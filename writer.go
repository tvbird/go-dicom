package dicom

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/msz-kp/go-dicom/dicomio"
	"github.com/msz-kp/go-dicom/dicomlog"
	"github.com/msz-kp/go-dicom/dicomtag"
)

// WriteOptSet represents the flattened option set after all WriteOptions have been applied.
type WriteOptSet struct {
	SkipVRVerification bool
}

func toWriteOptSet(opts ...WriteOption) *WriteOptSet {
	optSet := &WriteOptSet{}
	for _, opt := range opts {
		opt(optSet)
	}
	return optSet
}

// WriteOption represents an option that can be passed to WriteDataset. Later options will override previous options if
// applicable.
type WriteOption func(*WriteOptSet)

// SkipVRVerification returns a WriteOption that skips VR verification.
func SkipVRVerification() WriteOption {
	return func(set *WriteOptSet) {
		set.SkipVRVerification = true
	}
}

// WriteFileHeader produces a DICOM file header. metaElems[] is be a list of
// elements to be embedded in the header part.  Every element in metaElems[]
// must have Tag.Group==2. It must contain at least the following three
// elements: TagTransferSyntaxUID, TagMediaStorageSOPClassUID,
// TagMediaStorageSOPInstanceUID. The list may contain other meta elements as
// long as their Tag.Group==2; they are added to the header.
//
// Errors are reported via e.Error().
//
// Consult the following page for the DICOM file header format.
//
// http://dicom.nema.org/dicom/2013/output/chtml/part10/chapter_7.html
func WriteFileHeader(e *dicomio.Encoder, metaElems []*Element, opts *WriteOptSet) {
	e.PushTransferSyntax(binary.LittleEndian, dicomio.ExplicitVR)
	defer e.PopTransferSyntax()

	subEncoder := dicomio.NewBytesEncoder(binary.LittleEndian, dicomio.ExplicitVR)
	tagsUsed := make(map[dicomtag.Tag]bool)
	tagsUsed[dicomtag.FileMetaInformationGroupLength] = true
	writeRequiredMetaElem := func(tag dicomtag.Tag) {
		if elem, err := FindElementByTag(metaElems, tag); err == nil {
			WriteElement(subEncoder, elem, opts)
		} else {
			subEncoder.SetErrorf("%v not found in metaelems: %v", dicomtag.DebugString(tag), err)
		}
		tagsUsed[tag] = true
	}
	writeOptionalMetaElem := func(tag dicomtag.Tag, defaultValue interface{}) {
		if elem, err := FindElementByTag(metaElems, tag); err == nil {
			WriteElement(subEncoder, elem, opts)
		} else {
			WriteElement(subEncoder, MustNewElement(tag, defaultValue), opts)
		}
		tagsUsed[tag] = true
	}
	writeOptionalMetaElem(dicomtag.FileMetaInformationVersion, []byte("0 1"))
	writeRequiredMetaElem(dicomtag.MediaStorageSOPClassUID)
	writeRequiredMetaElem(dicomtag.MediaStorageSOPInstanceUID)
	writeRequiredMetaElem(dicomtag.TransferSyntaxUID)
	writeOptionalMetaElem(dicomtag.ImplementationClassUID, GoDICOMImplementationClassUID)
	writeOptionalMetaElem(dicomtag.ImplementationVersionName, GoDICOMImplementationVersionName)
	for _, elem := range metaElems {
		if elem.Tag.Group == dicomtag.MetadataGroup {
			if _, ok := tagsUsed[elem.Tag]; !ok {
				WriteElement(subEncoder, elem, opts)
			}
		}
	}
	if subEncoder.Error() != nil {
		e.SetError(subEncoder.Error())
		return
	}
	metaBytes := subEncoder.Bytes()
	e.WriteZeros(128)
	e.WriteString("DICM")
	WriteElement(e, MustNewElement(dicomtag.FileMetaInformationGroupLength, uint32(len(metaBytes))), opts)
	e.WriteBytes(metaBytes)
}

func encodeElementHeader(e *dicomio.Encoder, tag dicomtag.Tag, vr string, vl uint32) {
	doassert(vl == undefinedLength || vl%2 == 0, vl)
	e.WriteUInt16(tag.Group)
	e.WriteUInt16(tag.Element)

	_, implicit := e.TransferSyntax()
	if tag.Group == itemSeqGroup {
		implicit = dicomio.ImplicitVR
	}
	if implicit == dicomio.ExplicitVR {
		doassert(len(vr) == 2, vr)
		e.WriteString(vr)
		switch vr {
		case "NA", "OB", "OD", "OF", "OL", "OW", "SQ", "UN", "UC", "UR", "UT":
			e.WriteZeros(2) // two bytes for "future use" (0000H)
			e.WriteUInt32(vl)
		default:
			e.WriteUInt16(uint16(vl))
		}
	} else {
		doassert(implicit == dicomio.ImplicitVR, implicit)
		e.WriteUInt32(vl)
	}
}

func writeRawItem(e *dicomio.Encoder, data []byte) {
	encodeElementHeader(e, dicomtag.Item, "NA", uint32(len(data)))
	e.WriteBytes(data)
}

func writeBasicOffsetTable(e *dicomio.Encoder, offsets []uint32) {
	byteOrder, _ := e.TransferSyntax()
	subEncoder := dicomio.NewBytesEncoder(byteOrder, dicomio.ImplicitVR)
	for _, offset := range offsets {
		subEncoder.WriteUInt32(offset)
	}
	writeRawItem(e, subEncoder.Bytes())
}

func verifyVROrDefault(t dicomtag.Tag, vr string, opts *WriteOptSet) (string, error) {
	if vr != "" && opts.SkipVRVerification {
		return vr, nil
	}

	tagInfo, err := dicomtag.Find(t)
	if err != nil {
		if vr == "" {
			vr = "UN"
		}
		return vr, nil
	}
	if vr == "" {
		return tagInfo.VR, nil
	}

	// Verify the VR on the way out if the caller wants it.
	if !opts.SkipVRVerification && tagInfo.VR != vr {
		if dicomtag.GetVRKind(t, tagInfo.VR) != dicomtag.GetVRKind(t, vr) {
			return "", fmt.Errorf("ERROR dicomio.veryifyElement: VR mismatch for tag %v. Element.VR=%v, but DICOM standard defines VR to be %v",
				dicomtag.DebugString(t), vr, tagInfo.VR)
		}
		dicomlog.Vprintf(1, "dicom.WriteElement: VR value mismatch for tag %s. Element.VR=%v, but DICOM standard defines VR to be %v (continuing)",
			dicomtag.DebugString(t), vr, tagInfo.VR)
	}
	return vr, nil
}

// WriteElement encodes one data element.  Errors are reported through e.Error()
// and/or E.Finish().
//
// REQUIRES: Each value in values[] must match the VR of the tag. E.g., if tag
// is for UL, then each value must be uint32.

func WriteElement(e *dicomio.Encoder, elem *Element, opts *WriteOptSet) {
	vr, err := verifyVROrDefault(elem.Tag, elem.VR, opts)
	if err != nil {
		e.SetErrorf(err.Error())
		return
	}
	doassert(vr != "", vr)
	if elem.Tag == dicomtag.PixelData {
		if len(elem.Value) != 1 {
			// TODO(saito) Use of PixelDataInfo is a temp hack. Come up with a more proper solution.
			e.SetError(fmt.Errorf("PixelData element must have one value of type PixelDataInfo"))
		}
		image, ok := elem.Value[0].(PixelDataInfo)
		if !ok {
			e.SetError(fmt.Errorf("PixelData element must have one value of type PixelDataInfo"))
		}
		if elem.UndefinedLength {
			encodeElementHeader(e, elem.Tag, vr, undefinedLength)
			writeBasicOffsetTable(e, image.Offsets)
			for _, image := range image.Frames {
				writeRawItem(e, image)
			}
			encodeElementHeader(e, dicomtag.SequenceDelimitationItem, "" /*not used*/, 0)
		} else {
			doassert(len(image.Frames) == 1, image.Frames) // TODO
			encodeElementHeader(e, elem.Tag, vr, uint32(len(image.Frames[0])))
			e.WriteBytes(image.Frames[0])
		}
		return
	}
	if vr == "SQ" {
		if elem.UndefinedLength {
			encodeElementHeader(e, elem.Tag, vr, undefinedLength)
			for _, value := range elem.Value {
				subelem, ok := value.(*Element)
				if !ok || subelem.Tag != dicomtag.Item {
					e.SetError(fmt.Errorf("SQ element must be an Item, but found %v", value))
					return
				}
				WriteElement(e, subelem, opts)
			}
			encodeElementHeader(e, dicomtag.SequenceDelimitationItem, "" /*not used*/, 0)
		} else {
			sube := dicomio.NewBytesEncoder(e.TransferSyntax())
			for _, value := range elem.Value {
				subelem, ok := value.(*Element)
				if !ok || subelem.Tag != dicomtag.Item {
					e.SetErrorf("SQ element must be an Item, but found %v", value)
					return
				}
				WriteElement(sube, subelem, opts)
			}
			if sube.Error() != nil {
				e.SetError(sube.Error())
				return
			}
			bytes := sube.Bytes()
			encodeElementHeader(e, elem.Tag, vr, uint32(len(bytes)))
			e.WriteBytes(bytes)
		}
	} else if vr == "NA" { // Item
		if elem.UndefinedLength {
			encodeElementHeader(e, elem.Tag, vr, undefinedLength)
			for _, value := range elem.Value {
				subelem, ok := value.(*Element)
				if !ok {
					e.SetErrorf("Item values must be a dicom.Element, but found %v", value)
					return
				}
				WriteElement(e, subelem, opts)
			}
			encodeElementHeader(e, dicomtag.ItemDelimitationItem, "" /*not used*/, 0)
		} else {
			sube := dicomio.NewBytesEncoder(e.TransferSyntax())
			for _, value := range elem.Value {
				subelem, ok := value.(*Element)
				if !ok {
					e.SetErrorf("Item values must be a dicom.Element, but found %v", value)
					return
				}
				WriteElement(sube, subelem, opts)
			}
			if sube.Error() != nil {
				e.SetError(sube.Error())
				return
			}
			bytes := sube.Bytes()
			encodeElementHeader(e, elem.Tag, vr, uint32(len(bytes)))
			e.WriteBytes(bytes)
		}
	} else {
		if elem.UndefinedLength && vr != "UN" {
			e.SetErrorf("Encoding undefined-length element not yet supported: %v", elem)
			return

		}
		sube := dicomio.NewBytesEncoder(e.TransferSyntax())
		switch vr {
		case "US":
			for _, value := range elem.Value {
				v, ok := value.(uint16)
				if !ok {
					e.SetErrorf("%v: expect uint16, but found %v",
						dicomtag.DebugString(elem.Tag), value)
					continue
				}
				sube.WriteUInt16(v)
			}
		case "UL":
			for _, value := range elem.Value {
				v, ok := value.(uint32)
				if !ok {
					e.SetErrorf("%v: expect uint32, but found %v",
						dicomtag.DebugString(elem.Tag), value)
					continue
				}
				sube.WriteUInt32(v)
			}
		case "SL":
			for _, value := range elem.Value {
				v, ok := value.(int32)
				if !ok {
					e.SetErrorf("%v: expect int32, but found %v",
						dicomtag.DebugString(elem.Tag), value)
					continue
				}
				sube.WriteInt32(v)
			}
		case "SS":
			for _, value := range elem.Value {
				v, ok := value.(int16)
				if !ok {
					e.SetErrorf("%v: expect int16, but found %v",
						dicomtag.DebugString(elem.Tag), value)
					continue
				}
				sube.WriteInt16(v)
			}
		case "FL":
			fallthrough
		case "OF":
			for _, value := range elem.Value {
				v, ok := value.(float32)
				if !ok {
					e.SetErrorf("%v: expect float32, but found %v",
						dicomtag.DebugString(elem.Tag), value)
					continue
				}
				sube.WriteFloat32(v)
			}
		case "FD":
			fallthrough
		case "OD":
			for _, value := range elem.Value {
				v, ok := value.(float64)
				if !ok {
					e.SetErrorf("%v: expect float64, but found %v",
						dicomtag.DebugString(elem.Tag), value)
					continue
				}
				sube.WriteFloat64(v)
			}
		case "OW", "OB": // TODO(saito) Check that size is even. Byte swap??
			if len(elem.Value) != 1 {
				e.SetErrorf("%v: expect a single value but found %v",
					dicomtag.DebugString(elem.Tag), elem.Value)
				break
			}
			bytes, ok := elem.Value[0].([]byte)
			if !ok {
				e.SetErrorf("%v: expect a binary string but found %v",
					dicomtag.DebugString(elem.Tag), elem.Value[0])
				break
			}
			if vr == "OW" {
				if len(bytes)%2 != 0 {
					e.SetErrorf("%v: expect a binary string of even length, but found length %v",
						dicomtag.DebugString(elem.Tag), len(bytes))
					break
				}
				d := dicomio.NewBytesDecoder(bytes, dicomio.NativeByteOrder, dicomio.UnknownVR)
				n := len(bytes) / 2
				for i := 0; i < n; i++ {
					v := d.ReadUInt16()
					sube.WriteUInt16(v)
				}
				doassert(d.Finish() == nil, d.Error())
			} else { // vr=="OB"
				sube.WriteBytes(bytes)
				if len(bytes)%2 == 1 {
					sube.WriteByte(0)
				}
			}
		case "UI":
			s := ""
			for i, value := range elem.Value {
				var substr string
				switch v := value.(type) {
				case string:
					substr = v
				case dicomtag.Tag:
					substr = fmt.Sprintf("%04X%04X", v.Group, v.Element)
				case uint32:
					substr = fmt.Sprintf("%08X", v)
				case []byte:
					substr = string(v)
				default:
					// Преобразуем в строку любой другой тип
					substr = fmt.Sprintf("%v", v)
				}
				if i > 0 {
					s += "\\"
				}
				s += substr
			}
			sube.WriteString(s)
			if len(s)%2 == 1 {
				sube.WriteByte(0)
			}
		case "AT", "NA":
			fallthrough
		default:
			// dirty UN with undef len hack
			if elem.UndefinedLength && vr == "UN" {
				dicomlog.Vprintf(-1, "tag %s removed. (vr 'UN' with undefined length unsupported)", elem.Tag)
				break
			}

			s := ""
			for i, value := range elem.Value {
				var substr string
				switch v := value.(type) {
				case string:
					substr = v
				case dicomtag.Tag:
					substr = fmt.Sprintf("%04X%04X", v.Group, v.Element)
				case uint32:
					substr = fmt.Sprintf("%08X", v)
				case []byte:
					substr = string(v)
				default:
					// Преобразуем в строку любой другой тип
					substr = fmt.Sprintf("%v", v)
				}
				if i > 0 {
					s += "\\"
				}
				s += substr
			}
			sube.WriteString(s)
			if len(s)%2 == 1 {
				sube.WriteByte(' ')
			}
		}
		if sube.Error() != nil {
			e.SetError(sube.Error())
			return
		}
		bytes := sube.Bytes()
		encodeElementHeader(e, elem.Tag, vr, uint32(len(bytes)))
		e.WriteBytes(bytes)
	}
}

// WriteDataSet writes the dataset into the stream in DICOM file format,
// complete with the magic header and metadata elements.
//
// The transfer syntax (byte order, etc) of the file is determined by the
// TransferSyntax element in "ds". If ds is missing that or a few other
// essential elements, this function returns an error.
//
//	ds := ... read or create dicom.Dataset ...
//	out, err := os.Create("test.dcm")
//	err := dicom.Write(out, ds)
func WriteDataSet(out io.Writer, ds *DataSet, opts ...WriteOption) error {
	optSet := toWriteOptSet(opts...)
	e := dicomio.NewEncoder(out, nil, dicomio.UnknownVR)
	var metaElems []*Element
	for _, elem := range ds.Elements {
		if elem.Tag.Group == dicomtag.MetadataGroup {
			metaElems = append(metaElems, elem)
		}
	}
	WriteFileHeader(e, metaElems, optSet)
	if e.Error() != nil {
		return e.Error()
	}
	endian, implicit, err := getTransferSyntax(ds)
	if err != nil {
		return err
	}
	e.PushTransferSyntax(endian, implicit)
	for _, elem := range ds.Elements {
		// Пропускаем приватные теги (нечетная группа) и метаданные
		if elem.Tag.Group != dicomtag.MetadataGroup && elem.Tag.Group%2 == 0 {
			WriteElement(e, elem, optSet)
		}
	}
	e.PopTransferSyntax()
	return e.Error()
}

// WriteDataSetToFile writes "ds" to the given file. If the file already exists,
// existing contents are clobbered. Else, the file is newly created.
func WriteDataSetToFile(path string, ds *DataSet) error {
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := WriteDataSet(out, ds); err != nil {
		out.Close() // nolint: errcheck
		return err
	}
	return out.Close()
}
