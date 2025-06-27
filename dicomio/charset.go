package dicomio

import (
	"strings"

	"github.com/msz-kp/go-dicom/dicomlog"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/htmlindex"
)

// CodingSystem defines how a []byte is translated into a utf8 string.
type CodingSystem struct {
	// VR="PN" is the only place where we potentially use all three
	// decoders.  For all other VR types, only Ideographic decoder is used.
	// See P3.5, 6.2.
	//
	// P3.5 6.1 is supposed to define the coding systems in detail.  But the
	// spec text is insanely obtuse and I couldn't tell what its meaning
	// after hours of trying. So I just copied what pydicom charset.py is
	// doing.
	Alphabetic  *encoding.Decoder
	Ideographic *encoding.Decoder
	Phonetic    *encoding.Decoder
}

// CodingSystemType defines the where the coding system is going to be
// used. This distinction is useful in Japanese, but of little use in other
// languages.
type CodingSystemType int

const (
	// AlphabeticCodingSystem is for writing a name in (English) alphabets.
	AlphabeticCodingSystem CodingSystemType = iota
	// IdeographicCodingSystem is for writing the name in the native writing
	// system (Kanji).
	IdeographicCodingSystem
	// PhoneticCodingSystem is for hirakana and/or katakana.
	PhoneticCodingSystem
)

// Mapping of DICOM charset name to golang encoding/htmlindex name.  "" means
// 7bit ascii.
var htmlEncodingNames = map[string]string{
	"":                "", // Добавляем поддержку пустой строки (default ASCII)
	"ISO 2022 IR 6":   "iso-8859-1",
	"ISO_IR 13":       "shift_jis",
	"ISO 2022 IR 13":  "shift_jis",
	"ISO_IR 100":      "iso-8859-1",
	"ISO 2022 IR 100": "iso-8859-1",
	"ISO_IR 101":      "iso-8859-2",
	"ISO 2022 IR 101": "iso-8859-2",
	"ISO_IR 109":      "iso-8859-3",
	"ISO 2022 IR 109": "iso-8859-3",
	"ISO_IR 110":      "iso-8859-4",
	"ISO 2022 IR 110": "iso-8859-4",
	"ISO_IR 126":      "iso-ir-126",
	"ISO 2022 IR 126": "iso-ir-126",
	"ISO_IR 127":      "iso-ir-127",
	"ISO 2022 IR 127": "iso-ir-127",
	"ISO_IR 138":      "iso-ir-138",
	"ISO 2022 IR 138": "iso-ir-138",
	"ISO_IR 144":      "iso-8859-5", // Кириллица ISO-8859-5
	"ISO 2022 IR 144": "iso-8859-5", // Кириллица ISO-8859-5
	"ISO_IR 148":      "iso-ir-148",
	"ISO 2022 IR 148": "iso-ir-148",
	"ISO 2022 IR 149": "euc-kr",
	"ISO 2022 IR 159": "iso-2022-jp",
	"ISO_IR 166":      "iso-ir-166",
	"ISO 2022 IR 166": "iso-ir-166",
	"ISO 2022 IR 87":  "iso-2022-jp",
	"ISO_IR 192":      "utf-8",
	"GB18030":         "gb18030", // Добавляем поддержку GB18030
	"CP1250HACK":      "windows-1250",
	// Дополнительные кириллические кодировки
	"ISO_IR 146":      "koi8-r",       // КОИ8-Р
	"ISO 2022 IR 146": "koi8-r",       // КОИ8-Р
	"WINDOWS-1251":    "windows-1251", // Windows-1251
	"CP1251":          "windows-1251", // CP1251 (алиас для Windows-1251)
	"KOI8-R":          "koi8-r",       // КОИ8-Р (прямое указание)
	"KOI8-U":          "koi8-u",       // КОИ8-У (украинский)
	"CP866":           "ibm866",       // CP866 (DOS кириллица)
	"IBM866":          "ibm866",       // IBM866
}

// getCustomDecoder возвращает кастомный декодер для специальных кодировок
func getCustomDecoder(encodingName string) *encoding.Decoder {
	switch encodingName {
	case "iso-8859-5":
		return charmap.ISO8859_5.NewDecoder()
	case "koi8-r":
		return charmap.KOI8R.NewDecoder()
	case "koi8-u":
		return charmap.KOI8U.NewDecoder()
	case "windows-1251":
		return charmap.Windows1251.NewDecoder()
	case "windows-1250":
		return charmap.Windows1250.NewDecoder()
	case "ibm866":
		return charmap.CodePage866.NewDecoder()
	default:
		return nil
	}
}

// ParseSpecificCharacterSet converts DICOM character encoding names, such as
// "ISO-IR 100" to golang decoder. It will return nil, nil for the default (7bit
// ASCII) encoding. Cf. P3.2
// D.6.2. http://dicom.nema.org/medical/dicom/2016d/output/chtml/part02/sect_D.6.2.html
func ParseSpecificCharacterSet(encodingNames []string, CP1250Fix bool) (CodingSystem, error) {
	var decoders []*encoding.Decoder
	for _, name := range encodingNames {
		if CP1250Fix && name == "ISO_IR 100" {
			name = "CP1250HACK"
		}

		// Нормализуем имя кодировки перед поиском в мапе
		normalizedName := strings.TrimSpace(name)
		// Замена всех подряд идущих пробелов на один пробел
		normalizedName = strings.Join(strings.Fields(normalizedName), " ")

		var c *encoding.Decoder
		dicomlog.Vprintf(2, "dicom.ParseSpecificCharacterSet: Using coding system %s", normalizedName)

		if htmlName, ok := htmlEncodingNames[normalizedName]; !ok {
			// Попробуем найти кодировку по альтернативным именам
			if altDecoder := tryAlternativeEncodings(normalizedName); altDecoder != nil {
				c = altDecoder
				dicomlog.Vprintf(2, "dicom.ParseSpecificCharacterSet: Found alternative encoding for %s", normalizedName)
			} else {
				// TODO(saito) Support more encodings.
				dicomlog.Vprintf(1, "dicom.ParseSpecificCharacterSet: Unknown character set '%s'. Falling back to UTF-8", normalizedName)
				// Не возвращаем ошибку, а используем UTF-8 как fallback
				d, err := htmlindex.Get("utf-8")
				if err == nil {
					c = d.NewDecoder()
				}
			}
		} else {
			if htmlName != "" {
				// Сначала пробуем кастомный декодер
				if customDecoder := getCustomDecoder(htmlName); customDecoder != nil {
					c = customDecoder
				} else {
					// Если кастомного декодера нет, используем htmlindex
					d, err := htmlindex.Get(htmlName)
					if err != nil {
						dicomlog.Vprintf(1, "dicom.ParseSpecificCharacterSet: Encoding %s (for %s) not found in htmlindex, trying custom decoder", htmlName, normalizedName)
						// Попробуем кастомный декодер еще раз с оригинальным именем
						if customDecoder := getCustomDecoder(normalizedName); customDecoder != nil {
							c = customDecoder
						} else {
							// Fallback to UTF-8
							fallbackD, fallbackErr := htmlindex.Get("utf-8")
							if fallbackErr == nil {
								c = fallbackD.NewDecoder()
								dicomlog.Vprintf(1, "dicom.ParseSpecificCharacterSet: Using UTF-8 as fallback for %s", normalizedName)
							}
						}
					} else {
						c = d.NewDecoder()
					}
				}
			}
		}
		decoders = append(decoders, c)
	}

	if len(decoders) == 0 {
		return CodingSystem{nil, nil, nil}, nil
	}
	if len(decoders) == 1 {
		return CodingSystem{decoders[0], decoders[0], decoders[0]}, nil
	}
	if len(decoders) == 2 {
		return CodingSystem{decoders[0], decoders[1], decoders[1]}, nil
	}
	return CodingSystem{decoders[0], decoders[1], decoders[2]}, nil
}

// tryAlternativeEncodings пытается найти кодировку по альтернативным именам
func tryAlternativeEncodings(name string) *encoding.Decoder {
	upperName := strings.ToUpper(name)

	// Список альтернативных имен для кириллических кодировок
	alternatives := map[string]*encoding.Decoder{
		"CYRILLIC":    charmap.ISO8859_5.NewDecoder(),
		"ISO-8859-5":  charmap.ISO8859_5.NewDecoder(),
		"ISO8859-5":   charmap.ISO8859_5.NewDecoder(),
		"KOI8R":       charmap.KOI8R.NewDecoder(),
		"KOI-8-R":     charmap.KOI8R.NewDecoder(),
		"KOI8U":       charmap.KOI8U.NewDecoder(),
		"KOI-8-U":     charmap.KOI8U.NewDecoder(),
		"WIN-1251":    charmap.Windows1251.NewDecoder(),
		"WIN1251":     charmap.Windows1251.NewDecoder(),
		"WINDOWS1251": charmap.Windows1251.NewDecoder(),
		"CP-1251":     charmap.Windows1251.NewDecoder(),
		"CP-866":      charmap.CodePage866.NewDecoder(),
		"CP866":       charmap.CodePage866.NewDecoder(),
		"DOS-866":     charmap.CodePage866.NewDecoder(),
		"IBM-866":     charmap.CodePage866.NewDecoder(),
	}

	if decoder, exists := alternatives[upperName]; exists {
		return decoder
	}

	return nil
}
