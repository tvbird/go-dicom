package dicom

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/msz-kp/go-dicom/dicomio"
	"github.com/msz-kp/go-dicom/dicomtag"
	"golang.org/x/text/encoding/charmap"
)

// GoDICOMImplementationClassUIDPrefix defines the UID prefix for
// go-dicom. Provided by https://www.medicalconnections.co.uk/Free_UID
const GoDICOMImplementationClassUIDPrefix = "1.2.826.0.1.3680043.9.7133"

var GoDICOMImplementationClassUID = GoDICOMImplementationClassUIDPrefix + ".1.1"

const GoDICOMImplementationVersionName = "GODICOM_1_1"

// DataSet represents contents of one DICOM file.
type DataSet struct {
	// Elements in the file, in order of appearance.
	//
	// Note: unlike pydicom, Elements also contains meta elements (those
	// with Tag.Group==2).
	Elements []*Element
}

func doassert(cond bool, values ...interface{}) {
	if !cond {
		var s string
		for _, value := range values {
			s += fmt.Sprintf("%v ", value)
		}
		panic(s)
	}
}

// ReadOptions defines how DataSets and Elements are parsed.
type ReadOptions struct {
	// DropPixelData will cause the parser to skip the PixelData element
	// (bulk images) in ReadDataSet.
	DropPixelData bool

	// ReturnTags is a whitelist of tags to return.
	ReturnTags []dicomtag.Tag

	// StopAtag defines a tag at which when read (or a tag with a greater
	// value than it is read), the program will stop parsing the dicom file.
	StopAtTag *dicomtag.Tag

	// :/
	CP1250Fix bool

	// DefaultCyrillicEncoding - кодировка по умолчанию для кириллицы
	DefaultCyrillicEncoding string
}

// ReadDataSetInBytes is a shorthand for ReadDataSet(bytes.NewBuffer(data), len(data)).
//
// On parse error, this function may return a non-nil dataset and a non-nil
// error. In such case, the dataset will contain parts of the file that are
// parsable, and error will show the first error found by the parser.
func ReadDataSetInBytes(data []byte, options ReadOptions) (*DataSet, error) {
	return ReadDataSet(bytes.NewReader(data), options)
}

// ReadDataSetFromFile parses file cotents into dicom.DataSet. It is a thin
// wrapper around ReadDataSet.
//
// On parse error, this function may return a non-nil dataset and a non-nil
// error. In such case, the dataset will contain parts of the file that are
// parsable, and error will show the first error found by the parser.
func ReadDataSetFromFile(path string, options ReadOptions) (*DataSet, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	ds, err := ReadDataSet(file, options)
	if e := file.Close(); e != nil && err == nil {
		err = e
	}
	return ds, err
}

// detectCyrillicEncoding пытается определить кириллическую кодировку
func detectCyrillicEncoding(text string, defaultEncoding string) string {
	// Если уже UTF-8, возвращаем как есть
	if utf8.ValidString(text) {
		return text
	}

	// Список кодировок для проверки
	encodings := []struct {
		name    string
		decoder *charmap.Charmap
	}{
		{"windows-1251", charmap.Windows1251},
		{"koi8-r", charmap.KOI8R},
		{"iso-8859-5", charmap.ISO8859_5},
		{"cp866", charmap.CodePage866},
	}

	// Если указана кодировка по умолчанию, проверяем её первой
	if defaultEncoding != "" {
		for _, enc := range encodings {
			if enc.name == defaultEncoding {
				if decoded, err := enc.decoder.NewDecoder().String(text); err == nil {
					if containsCyrillic(decoded) {
						return decoded
					}
				}
				break
			}
		}
	}

	// Пробуем все кодировки
	for _, enc := range encodings {
		if enc.name == defaultEncoding {
			continue // уже проверили выше
		}

		if decoded, err := enc.decoder.NewDecoder().String(text); err == nil {
			if containsCyrillic(decoded) {
				return decoded
			}
		}
	}

	// Если ничего не помогло, возвращаем исходный текст
	return text
}

// containsCyrillic проверяет, содержит ли строка кириллические символы
func containsCyrillic(text string) bool {
	for _, r := range text {
		if (r >= 0x0400 && r <= 0x04FF) || // Cyrillic
			(r >= 0x0500 && r <= 0x052F) || // Cyrillic Supplement
			(r >= 0x2DE0 && r <= 0x2DFF) || // Cyrillic Extended-A
			(r >= 0xA640 && r <= 0xA69F) { // Cyrillic Extended-B
			return true
		}
	}
	return false
}

// processMultiValueDSElement обрабатывает элементы типа DS с множественными значениями
func processMultiValueDSElement(elem *Element) {
	if elem.Value != nil && len(elem.Value) == 1 {
		if strVal, ok := elem.Value[0].(string); ok {
			// Проверяем, содержит ли строка разделители
			if strings.Contains(strVal, "\\") {
				// Разбиваем строку по разделителям
				parts := strings.Split(strVal, "\\")
				newValues := make([]interface{}, len(parts))
				for i, part := range parts {
					newValues[i] = strings.TrimSpace(part)
				}
				elem.Value = newValues
			}
		}
	}
}

// ReadDataSet reads a DICOM file from "io".
//
// On parse error, this function may return a non-nil dataset and a non-nil
// error. In such case, the dataset will contain parts of the file that are
// parsable, and error will show the first error found by the parser.
func ReadDataSet(in io.Reader, options ReadOptions) (*DataSet, error) {
	buffer := dicomio.NewDecoder(in, binary.LittleEndian, dicomio.ExplicitVR)
	metaElems := ParseFileHeader(buffer)
	if buffer.Error() != nil {
		return nil, buffer.Error()
	}
	file := &DataSet{Elements: metaElems}

	// Change the transfer syntax for the rest of the file.
	endian, implicit, err := getTransferSyntax(file)
	if err != nil {
		return nil, err
	}
	buffer.PushTransferSyntax(endian, implicit)
	defer buffer.PopTransferSyntax()

	// Флаг для отслеживания, была ли установлена кодировка
	charsetSet := false

	// Read the list of elements.
	for !buffer.EOF() {
		startLen := buffer.BytesRead()
		elem := ReadElement(buffer, options)
		if buffer.BytesRead() <= startLen { // Avoid silent infinite looping.
			panic(fmt.Sprintf("ReadElement failed to consume data: position %d: %v", startLen, buffer.Error()))
		}
		if elem == endOfDataElement {
			// element is a pixel data and was dropped by options
			break
		}
		if elem == nil {
			// Parse error.
			continue
		}
		if elem.Tag == dicomtag.SpecificCharacterSet {
			// Set the []byte -> string decoder for the rest of the
			// file.  It's sad that SpecificCharacterSet isn't part
			// of metadata, but is part of regular attrs, so we need
			// to watch out for multiple occurrences of this type of
			// elements.
			encodingNames, err := elem.GetCleanStrings()
			if err != nil {
				buffer.SetError(err)
			} else {
				// TODO(saito) SpecificCharacterSet may appear in a
				// middle of a SQ or NA.  In such case, the charset seem
				// to be scoped inside the SQ or NA. So we need to make
				// the charset a stack.
				cs, err := dicomio.ParseSpecificCharacterSet(encodingNames, options.CP1250Fix)
				if err != nil {
					buffer.SetError(err)
				} else {
					buffer.SetCodingSystem(cs)
					charsetSet = true
				}
			}
		}

		// Если это строковый элемент и кодировка не была установлена,
		// пытаемся автоматически определить кириллическую кодировку
		if !charsetSet && elem.Value != nil && len(elem.Value) > 0 {
			if strVal, ok := elem.Value[0].(string); ok && strVal != "" {
				// Проверяем, есть ли кракозябры (неправильно декодированные символы)
				if containsGarbage(strVal) {
					// Пытаемся декодировать с разными кириллическими кодировками
					decoded := detectCyrillicEncoding(strVal, options.DefaultCyrillicEncoding)
					if decoded != strVal {
						elem.Value = []interface{}{decoded}
					}
				}
			}
		}

		// Обрабатываем элементы типа DS с множественными значениями
		if elem.VR == "DS" {
			processMultiValueDSElement(elem)
		}

		if options.ReturnTags == nil || (options.ReturnTags != nil && tagInList(elem.Tag, options.ReturnTags)) {
			// Очистка строковых значений от непечатаемых символов, сохраняя множественные значения
			if elem.Value != nil {
				cleanValues := make([]interface{}, len(elem.Value))
				for i, value := range elem.Value {
					if strVal, ok := value.(string); ok {
						cleanValues[i] = FilterNonPrintable(strVal)
					} else {
						cleanValues[i] = value
					}
				}
				elem.Value = cleanValues
			}
			file.Elements = append(file.Elements, elem)
		}
	}
	return file, buffer.Error()
}

// containsGarbage проверяет, содержит ли строка "кракозябры"
func containsGarbage(s string) bool {
	garbageCount := 0
	totalRunes := 0

	for _, r := range s {
		totalRunes++
		// Символы � обычно указывают на проблемы с кодировкой
		if r == '�' || r == '\uFFFD' {
			garbageCount++
		}
		// Подозрительные последовательности байтов, характерные для неправильно декодированной кириллицы
		if r >= 0x80 && r <= 0xFF {
			garbageCount++
		}
	}

	// Если больше 20% символов выглядят как кракозябры
	return totalRunes > 0 && float64(garbageCount)/float64(totalRunes) > 0.2
}

func FilterNonPrintable(s string) string {
	result := ""
	for _, r := range s {
		if unicode.IsPrint(r) || r == ' ' || r == '\t' {
			result += string(r)
		}
	}
	return result
}

func (e *Element) GetCleanString() (string, error) {
	s, err := e.GetString()
	if err != nil {
		return "", err
	}
	return FilterNonPrintable(s), nil
}

func (e *Element) GetCleanStrings() ([]string, error) {
	strs, err := e.GetStrings()
	if err != nil {
		return nil, err
	}

	cleanStrs := make([]string, len(strs))
	for i, s := range strs {
		cleanStrs[i] = FilterNonPrintable(s)
	}
	return cleanStrs, nil
}

func getTransferSyntax(ds *DataSet) (bo binary.ByteOrder, implicit dicomio.IsImplicitVR, err error) {
	elem, err := ds.FindElementByTag(dicomtag.TransferSyntaxUID)
	if err != nil {
		return nil, dicomio.UnknownVR, err
	}
	transferSyntaxUID, err := elem.GetCleanString()
	if err != nil {
		return nil, dicomio.UnknownVR, err
	}
	return dicomio.ParseTransferSyntaxUID(transferSyntaxUID)
}

// FindElementByName finds an element from the dataset given the element name,
// such as "PatientName".
func (f *DataSet) FindElementByName(name string) (*Element, error) {
	return FindElementByName(f.Elements, name)
}

// FindElementByTag finds an element from the dataset given its tag, such as
// Tag{0x0010, 0x0010}.
func (f *DataSet) FindElementByTag(tag dicomtag.Tag) (*Element, error) {
	return FindElementByTag(f.Elements, tag)
}
