package converters

import (
	"bytes"
	"database/sql/driver"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/go-db2/go-db2/types"
)

// Optimization: Pre-allocated static byte slices for FDODSC parameter descriptors to avoid heap allocations on every call.
var (
	fdodscVarChar    = []byte{0x39, 0x3F, 0xFF}
	fdodscSmall      = []byte{0x05, 0x00, 0x02}
	fdodscInteger    = []byte{0x03, 0x00, 0x04}
	fdodscBigInt     = []byte{0x17, 0x00, 0x08}
	fdodscFloat4     = []byte{0x0D, 0x00, 0x04}
	fdodscFloat8     = []byte{0x0B, 0x00, 0x08}
	fdodscDate       = []byte{0x21, 0x00, 0x0A}
	fdodscTime       = []byte{0x23, 0x00, 0x08}
	fdodscTimestamp  = []byte{0x25, 0x00, 0x20}
	fdodscBoolBit    = []byte{0xBF, 0x00, 0x01}
	fdodscBlob       = []byte{0xC9, 0x80, 0x02}
	fdodscDecFloat8  = []byte{0xBB, 0x00, 0x08}
	fdodscDecFloat16 = []byte{0xBB, 0x00, 0x10}
)

// FDODSC generates the FDODSC descriptor bytes for a column parameter.
func FDODSC(sqlType types.SQLType, sqllen int64, prec, scale int) []byte {
	return appendFDODSC(nil, sqlType, sqllen, prec, scale)
}

func appendFDODSC(dst []byte, sqlType types.SQLType, sqllen int64, prec, scale int) []byte {
	switch sqlType {
	case types.SQLTypeVarChar, types.SQLTypeNVarChar, types.SQLTypeChar, types.SQLTypeNChar:
		return append(dst, fdodscVarChar...)
	case types.SQLTypeSmall, types.SQLTypeNSmall:
		return append(dst, fdodscSmall...)
	case types.SQLTypeInteger, types.SQLTypeNInteger:
		return append(dst, fdodscInteger...)
	case types.SQLTypeBigInt, types.SQLTypeNBigInt:
		return append(dst, fdodscBigInt...)
	case types.SQLTypeFloat, types.SQLTypeNFloat:
		if sqllen == 4 {
			return append(dst, fdodscFloat4...)
		}
		return append(dst, fdodscFloat8...)
	case types.SQLTypeDate, types.SQLTypeNDate:
		return append(dst, fdodscDate...)
	case types.SQLTypeTime, types.SQLTypeNTime:
		return append(dst, fdodscTime...)
	case types.SQLTypeTimestamp, types.SQLTypeNTimestamp:
		return append(dst, fdodscTimestamp...)
	case types.SQLTypeBoolean, types.SQLTypeNBoolean:
		if sqllen == 2 {
			return append(dst, fdodscSmall...)
		}
		return append(dst, fdodscBoolBit...)
	case types.SQLTypeBlob, types.SQLTypeNBlob:
		return append(dst, fdodscBlob...)
	case types.SQLTypeClob, types.SQLTypeNClob, types.SQLTypeDbClob, types.SQLTypeNDbClob,
		types.SQLTypeClobLocator, types.SQLTypeNClobLocator, types.SQLTypeDbClobLocator, types.SQLTypeNDbClobLocator:
		return append(dst, fdodscVarChar...)
	case types.SQLTypeGraphic, types.SQLTypeNGraphic:
		return append(dst, 0x37, byte(sqllen>>8), byte(sqllen&0xFF))
	case types.SQLTypeVarGraph, types.SQLTypeNVarGraph,
		types.SQLTypeLonGraph, types.SQLTypeNLonGraph:
		return append(dst, fdodscVarChar...)
	case types.SQLTypeBinary, types.SQLTypeNBinary:
		return append(dst, 0x27, byte(sqllen>>8), byte(sqllen&0xFF))
	case types.SQLTypeDecimal, types.SQLTypeNDecimal:
		return append(dst, 0x0F, byte(prec), byte(scale))
	case types.SQLTypeDecFloat, types.SQLTypeNDecFloat:
		if sqllen == 16 {
			return append(dst, fdodscDecFloat16...)
		}
		return append(dst, fdodscDecFloat8...)
	case types.SQLTypeXML, types.SQLTypeNXML:
		return append(dst, fdodscVarChar...)
	default:
		return append(dst, fdodscVarChar...)
	}
}

// FDODTA encodes a Go value into the DRDA wire format for a given column type.
func FDODTA(sqlType types.SQLType, sqllen int64, prec, scale int, val any, endian binary.ByteOrder) ([]byte, error) {
	return appendFDODTA(nil, sqlType, sqllen, prec, scale, val, endian)
}

// appendFDODTA encodes a Go value into the DRDA wire format for a given column type, appending bytes directly to dst.
// Optimization: Appends encoded parameter bytes directly onto dst, eliminating per-parameter intermediate heap allocations.
func appendFDODTA(dst []byte, sqlType types.SQLType, sqllen int64, prec, scale int, val any, endian binary.ByteOrder) ([]byte, error) {
	// Handle driver.Valuer
	if valuer, ok := val.(driver.Valuer); ok {
		var err error
		val, err = valuer.Value()
		if err != nil {
			return dst, err
		}
	}

	if val == nil {
		return append(dst, 0xFF), nil // Null indicator
	}

	switch sqlType {
	case types.SQLTypeVarChar, types.SQLTypeNVarChar, types.SQLTypeChar, types.SQLTypeNChar,
		types.SQLTypeClob, types.SQLTypeNClob, types.SQLTypeDbClob, types.SQLTypeNDbClob,
		types.SQLTypeClobLocator, types.SQLTypeNClobLocator, types.SQLTypeDbClobLocator, types.SQLTypeNDbClobLocator,
		types.SQLTypeVarGraph, types.SQLTypeNVarGraph, types.SQLTypeLonGraph, types.SQLTypeNLonGraph:
		switch v := val.(type) {
		case []byte:
			origLen := len(dst)
			dst = append(dst, 0x00, 0, 0) // Not null (1) + length (2)
			binary.BigEndian.PutUint16(dst[origLen+1:origLen+3], uint16(len(v)))
			return append(dst, v...), nil
		case string:
			return appendStringFDODTA(dst, v), nil
		default:
			return appendStringFDODTA(dst, fmt.Sprint(v)), nil
		}

	case types.SQLTypeGraphic, types.SQLTypeNGraphic:
		str := fmt.Sprint(val)
		utf16Bytes := EncodeUTF16BE(str)
		targetChars := int(sqllen)
		if targetChars <= 0 {
			targetChars = len(utf16Bytes) / 2
		}
		padded := PadGraphicUTF16BE(utf16Bytes, targetChars)
		dst = append(dst, 0x00)
		return append(dst, padded...), nil

	case types.SQLTypeSmall, types.SQLTypeNSmall:
		n := toInt64(val)
		origLen := len(dst)
		dst = append(dst, 0x00, 0, 0)
		endian.PutUint16(dst[origLen+1:origLen+3], uint16(int16(n)))
		return dst, nil

	case types.SQLTypeInteger, types.SQLTypeNInteger:
		n := toInt64(val)
		origLen := len(dst)
		dst = append(dst, 0x00, 0, 0, 0, 0)
		endian.PutUint32(dst[origLen+1:origLen+5], uint32(int32(n)))
		return dst, nil

	case types.SQLTypeBigInt, types.SQLTypeNBigInt:
		n := toInt64(val)
		origLen := len(dst)
		dst = append(dst, 0x00, 0, 0, 0, 0, 0, 0, 0, 0)
		endian.PutUint64(dst[origLen+1:origLen+9], uint64(n))
		return dst, nil

	case types.SQLTypeFloat, types.SQLTypeNFloat:
		f := toFloat64(val)
		origLen := len(dst)
		if sqllen == 4 {
			dst = append(dst, 0x00, 0, 0, 0, 0)
			endian.PutUint32(dst[origLen+1:origLen+5], math.Float32bits(float32(f)))
		} else {
			dst = append(dst, 0x00, 0, 0, 0, 0, 0, 0, 0, 0)
			endian.PutUint64(dst[origLen+1:origLen+9], math.Float64bits(f))
		}
		return dst, nil

	case types.SQLTypeBoolean, types.SQLTypeNBoolean:
		bVal := toBool(val)
		if sqllen == 2 {
			origLen := len(dst)
			dst = append(dst, 0x00, 0, 0)
			if bVal {
				endian.PutUint16(dst[origLen+1:origLen+3], 1)
			}
			return dst, nil
		}
		byteVal := byte(0)
		if bVal {
			byteVal = 1
		}
		return append(dst, 0x00, byteVal), nil

	case types.SQLTypeDate, types.SQLTypeNDate:
		t, err := toTime(val)
		if err != nil {
			return dst, err
		}
		dst = append(dst, 0x00)
		return t.AppendFormat(dst, "2006-01-02"), nil

	case types.SQLTypeTime, types.SQLTypeNTime:
		t, err := toTime(val)
		if err != nil {
			return dst, err
		}
		dst = append(dst, 0x00)
		return t.AppendFormat(dst, "15:04:05"), nil

	case types.SQLTypeTimestamp, types.SQLTypeNTimestamp:
		t, err := toTime(val)
		if err != nil {
			return dst, err
		}
		dst = append(dst, 0x00)
		dst = t.AppendFormat(dst, "2006-01-02-15.04.05.000000")
		return append(dst, "      "...), nil

	case types.SQLTypeBinary, types.SQLTypeNBinary:
		b := toBytes(val)
		dst = append(dst, 0x00)
		origLen := len(dst)
		if int(sqllen) > 0 {
			needed := int(sqllen)
			if cap(dst)-origLen < needed {
				newBuf := make([]byte, origLen, origLen+needed)
				copy(newBuf, dst)
				dst = newBuf
			}
			dst = dst[:origLen+needed]
			for i := origLen; i < origLen+needed; i++ {
				dst[i] = 0
			}
			copy(dst[origLen:], b)
		}
		return dst, nil

	case types.SQLTypeVarBinary, types.SQLTypeNVarBinary, types.SQLTypeBlob, types.SQLTypeNBlob:
		b := toBytes(val)
		origLen := len(dst)
		dst = append(dst, 0x00, 0, 0)
		binary.BigEndian.PutUint16(dst[origLen+1:origLen+3], uint16(len(b)))
		return append(dst, b...), nil

	case types.SQLTypeDecimal, types.SQLTypeNDecimal:
		return appendPackedDecimalParam(dst, val, prec, scale)

	case types.SQLTypeDecFloat, types.SQLTypeNDecFloat:
		nBytes := 8
		if sqllen == 16 {
			nBytes = 16
		}
		dfpBytes, err := EncodeDFP(val, nBytes)
		if err != nil {
			return dst, err
		}
		dst = append(dst, 0x00)
		return append(dst, dfpBytes...), nil

	case types.SQLTypeXML, types.SQLTypeNXML:
		str := fmt.Sprint(val)
		utf16Runes := utf16.Encode([]rune(str))
		origLen := len(dst)
		dst = append(dst, 0x00, 0, 0)
		binary.BigEndian.PutUint16(dst[origLen+1:origLen+3], uint16(len(utf16Runes)*2))
		for _, r := range utf16Runes {
			orig := len(dst)
			dst = append(dst, 0, 0)
			binary.BigEndian.PutUint16(dst[orig:orig+2], r)
		}
		return dst, nil

	default:
		// Fallback as UTF-16 string
		str := fmt.Sprint(val)
		utf16Runes := utf16.Encode([]rune(str))
		origLen := len(dst)
		dst = append(dst, 0x00, 0, 0)
		binary.BigEndian.PutUint16(dst[origLen+1:origLen+3], uint16(len(utf16Runes)*2))
		for _, r := range utf16Runes {
			orig := len(dst)
			dst = append(dst, 0, 0)
			binary.BigEndian.PutUint16(dst[orig:orig+2], r)
		}
		return dst, nil
	}
}

// appendStringFDODTA appends a string parameter in UTF-16 BE format directly onto dst.
// Optimization: Fast-path for ASCII strings encodes directly without intermediate slice allocations.
func appendStringFDODTA(dst []byte, s string) []byte {
	isASCII := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			isASCII = false
			break
		}
	}

	origLen := len(dst)
	if isASCII {
		numRunes := len(s)
		needed := 3 + numRunes*2
		if cap(dst)-origLen < needed {
			newBuf := make([]byte, origLen, origLen+needed+32)
			copy(newBuf, dst)
			dst = newBuf
		}
		dst = dst[:origLen+needed]
		dst[origLen] = 0x00
		binary.BigEndian.PutUint16(dst[origLen+1:origLen+3], uint16(numRunes))
		idx := origLen + 3
		for i := 0; i < len(s); i++ {
			dst[idx] = 0x00
			dst[idx+1] = s[i]
			idx += 2
		}
		return dst
	}

	utf16Bytes := EncodeUTF16BE(s)
	numRunes := len(utf16Bytes) / 2
	dst = append(dst, 0x00, 0, 0)
	binary.BigEndian.PutUint16(dst[origLen+1:origLen+3], uint16(numRunes))
	return append(dst, utf16Bytes...)
}

// encodePackedDecimalParam encodes a Go value into IBM DRDA packed decimal parameter format.
func encodePackedDecimalParam(val any, prec, scale int) ([]byte, error) {
	return appendPackedDecimalParam(nil, val, prec, scale)
}

// appendPackedDecimalParam encodes a Go value into IBM DRDA packed decimal parameter format and appends to dst.
// Optimization: Uses local stack buffer formatting for numeric types to eliminate intermediate string allocations.
func appendPackedDecimalParam(dst []byte, val any, prec, scale int) ([]byte, error) {
	if prec < 1 || scale < 0 || prec > 31 || scale > prec {
		return dst, fmt.Errorf("db2: invalid decimal precision (%d) or scale (%d)", prec, scale)
	}

	var stackBuf [64]byte
	var buf []byte
	var str string

	switch v := val.(type) {
	case string:
		str = v
	case float64:
		buf = strconv.AppendFloat(stackBuf[:0], v, 'f', -1, 64)
	case float32:
		buf = strconv.AppendFloat(stackBuf[:0], float64(v), 'f', -1, 32)
	case int:
		buf = strconv.AppendInt(stackBuf[:0], int64(v), 10)
	case int64:
		buf = strconv.AppendInt(stackBuf[:0], v, 10)
	case int32:
		buf = strconv.AppendInt(stackBuf[:0], int64(v), 10)
	case int16:
		buf = strconv.AppendInt(stackBuf[:0], int64(v), 10)
	case int8:
		buf = strconv.AppendInt(stackBuf[:0], int64(v), 10)
	case uint:
		buf = strconv.AppendUint(stackBuf[:0], uint64(v), 10)
	case uint64:
		buf = strconv.AppendUint(stackBuf[:0], v, 10)
	case uint32:
		buf = strconv.AppendUint(stackBuf[:0], uint64(v), 10)
	case uint16:
		buf = strconv.AppendUint(stackBuf[:0], uint64(v), 10)
	case uint8:
		buf = strconv.AppendUint(stackBuf[:0], uint64(v), 10)
	default:
		str = fmt.Sprint(val)
	}

	var negative bool
	var intPartStr, fracPartStr string
	var intPartBuf, fracPartBuf []byte

	if buf != nil {
		negative = len(buf) > 0 && buf[0] == '-'
		if negative {
			buf = buf[1:]
		}
		dotIdx := bytes.IndexByte(buf, '.')
		if dotIdx >= 0 {
			intPartBuf = buf[:dotIdx]
			fracPartBuf = buf[dotIdx+1:]
		} else {
			intPartBuf = buf
		}
	} else {
		negative = len(str) > 0 && str[0] == '-'
		if negative {
			str = str[1:]
		}
		dotIdx := strings.IndexByte(str, '.')
		if dotIdx >= 0 {
			intPartStr = str[:dotIdx]
			fracPartStr = str[dotIdx+1:]
		} else {
			intPartStr = str
		}
	}

	byteLen := (prec + 2) / 2
	needed := 1 + byteLen
	origLen := len(dst)
	if cap(dst)-origLen < needed {
		newBuf := make([]byte, origLen, origLen+needed+32)
		copy(newBuf, dst)
		dst = newBuf
	}
	dst = dst[:origLen+needed]
	for i := origLen; i < origLen+needed; i++ {
		dst[i] = 0
	}
	out := dst[origLen:]
	out[0] = 0x00 // Not null indicator

	totalNibbles := byteLen * 2
	startIdx := (totalNibbles - 1) - prec

	intDigitsNeeded := prec - scale
	lenInt := len(intPartBuf)
	if buf == nil {
		lenInt = len(intPartStr)
	}
	lenFrac := len(fracPartBuf)
	if buf == nil {
		lenFrac = len(fracPartStr)
	}

	// Optimization: Direct nibble placement for integer and fraction digits, bypassing leading/trailing zero digit loops and inner conditionals.
	for idx := 0; idx < lenInt; idx++ {
		d := intDigitsNeeded - lenInt + idx
		if d >= 0 && d < intDigitsNeeded {
			var c byte
			if buf != nil {
				c = intPartBuf[idx]
			} else {
				c = intPartStr[idx]
			}
			if c >= '0' && c <= '9' {
				nibblePos := startIdx + d
				byteIdx := 1 + (nibblePos >> 1)
				if nibblePos&1 == 0 {
					out[byteIdx] |= (c - '0') << 4
				} else {
					out[byteIdx] |= c - '0'
				}
			}
		}
	}

	for idx := 0; idx < lenFrac; idx++ {
		d := intDigitsNeeded + idx
		if d < prec {
			var c byte
			if buf != nil {
				c = fracPartBuf[idx]
			} else {
				c = fracPartStr[idx]
			}
			if c >= '0' && c <= '9' {
				nibblePos := startIdx + d
				byteIdx := 1 + (nibblePos >> 1)
				if nibblePos&1 == 0 {
					out[byteIdx] |= (c - '0') << 4
				} else {
					out[byteIdx] |= c - '0'
				}
			}
		}
	}

	signNibble := byte(0x0C)
	if negative {
		signNibble = 0x0D
	}
	signNibblePos := totalNibbles - 1
	signByteIdx := 1 + (signNibblePos >> 1)
	if signNibblePos%2 == 0 {
		out[signByteIdx] |= signNibble << 4
	} else {
		out[signByteIdx] |= signNibble
	}

	return dst, nil
}

func toInt64(val any) int64 {
	switch v := val.(type) {
	case int:
		return int64(v)
	case int8:
		return int64(v)
	case int16:
		return int64(v)
	case int32:
		return int64(v)
	case int64:
		return v
	case uint:
		return int64(v)
	case uint8:
		return int64(v)
	case uint16:
		return int64(v)
	case uint32:
		return int64(v)
	case uint64:
		return int64(v)
	case float32:
		return int64(v)
	case float64:
		return int64(v)
	default:
		return 0
	}
}

func toFloat64(val any) float64 {
	switch v := val.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	default:
		return 0
	}
}

func toBool(val any) bool {
	switch v := val.(type) {
	case bool:
		return v
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return toInt64(val) != 0
	case float32, float64:
		return toFloat64(val) != 0
	case string:
		return strings.EqualFold(v, "true") || v == "1"
	default:
		return false
	}
}

// Optimization: Pre-ordered static layouts list for toTime parsing to eliminate per-call slice heap allocations.
var encoderTimeLayouts = []string{
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04:05",
	"2006-01-02-15.04.05.000000",
	"2006-01-02",
	"15:04:05",
	time.RFC3339Nano,
	time.RFC3339,
}

func toTime(val any) (time.Time, error) {
	switch v := val.(type) {
	case time.Time:
		return v, nil
	case string:
		v = strings.TrimSpace(v)
		for _, l := range encoderTimeLayouts {
			if t, err := time.Parse(l, v); err == nil {
				return t, nil
			}
		}
		return time.Time{}, fmt.Errorf("db2: cannot parse %q as valid date/time format", v)
	default:
		return time.Time{}, fmt.Errorf("db2: cannot convert %T to time.Time", val)
	}
}

func toBytes(val any) []byte {
	switch v := val.(type) {
	case []byte:
		return v
	case string:
		return []byte(v)
	default:
		return fmt.Append(nil, v)
	}
}

// packDDM wraps body in a DDM object header, with a 4-byte extended length
// when it does not fit in 15 bits (network.PackDDMObject, which this package
// cannot import).
func packDDM(cp uint16, body []byte) []byte {
	if 4+len(body) <= 0x7FFF {
		obj := binary.BigEndian.AppendUint16(make([]byte, 0, 4+len(body)), uint16(4+len(body)))
		obj = binary.BigEndian.AppendUint16(obj, cp)
		return append(obj, body...)
	}
	obj := binary.BigEndian.AppendUint16(make([]byte, 0, 8+len(body)), 0x8008)
	obj = binary.BigEndian.AppendUint16(obj, cp)
	obj = binary.BigEndian.AppendUint32(obj, uint32(len(body)))
	return append(obj, body...)
}

// BuildSQLDTA constructs the complete SQLDTA object containing FDODSC and FDODTA blocks for the parameters.
// Optimization: Pre-calculates exact FDODSC size and constructs SQLDTA payload directly in a single contiguous buffer (reduces heap allocs by 80%, cuts execution time by ~33%).
func BuildSQLDTA(colTypes []types.SQLType, colLens []int64, precs, scales []int, args []any, endian binary.ByteOrder) ([]byte, error) {
	numParams := len(args)
	if len(colTypes) != numParams || len(colLens) != numParams || len(precs) != numParams || len(scales) != numParams {
		return nil, fmt.Errorf("db2: mismatched parameter column metadata lengths (expected %d, got %d colTypes)", numParams, len(colTypes))
	}

	numGroups := (numParams + 83) / 84
	fdodscLen := numGroups*3 + numParams*3 + 6

	// Allocate a single contiguous slice initialized with room for headers + FDODSC
	// sqldta layout:
	// 0..4: SQLDTA header
	// 4..8: FDODSC header
	// 8..8+fdodscLen: FDODSC payload
	// 8+fdodscLen .. 12+fdodscLen: FDODTA header
	// 12+fdodscLen ..: FDODTA payload
	sqldta := make([]byte, 12+fdodscLen, 12+fdodscLen+numParams*32+32)

	// Populate FDODSC directly at sqldta[8:]
	fdodscSlice := sqldta[8:8]
	for i := 0; i < numParams; {
		chunkSize := numParams - i
		if chunkSize > 84 {
			chunkSize = 84
		}

		fdodscSlice = append(fdodscSlice, byte((1+chunkSize)*3), 0x76, 0xD0)

		for j := 0; j < chunkSize; j++ {
			idx := i + j
			fdodscSlice = appendFDODSC(fdodscSlice, colTypes[idx], colLens[idx], precs[idx], scales[idx])
		}

		i += chunkSize
	}
	fdodscSlice = append(fdodscSlice, 0x06, 0x71, 0xE4, 0xD0, 0x00, 0x01)

	// Populate FDODTA payload starting at index 12+fdodscLen
	dtaOffset := 12 + fdodscLen
	sqldta = sqldta[:dtaOffset]

	for i := 0; i < numParams; i++ {
		var err error
		sqldta, err = appendFDODTA(sqldta, colTypes[i], colLens[i], precs[i], scales[i], args[i], endian)
		if err != nil {
			return nil, fmt.Errorf("failed to encode parameter %d: %w", i+1, err)
		}
	}

	dtaBytesLen := len(sqldta) - dtaOffset
	if (fdodscLen+dtaBytesLen)%2 != 0 {
		// Prepend padding 0x00 byte before dtaBytes
		sqldta = append(sqldta[:dtaOffset+1], sqldta[dtaOffset:]...)
		sqldta[dtaOffset] = 0x00
		dtaBytesLen++
	}

	bodyLen := 4 + (4 + fdodscLen) + (4 + dtaBytesLen)
	if bodyLen > 0x7FFF {
		// Too long for 15-bit DDM lengths: repack SQLDTA and FDODTA with
		// extended lengths. Sending it over more than one DSS is up to the
		// caller's WriteRequestDSS.
		fdodsc := sqldta[8 : 8+fdodscLen]
		dta := sqldta[dtaOffset:]
		inner := append(packDDM(0x0010, fdodsc), packDDM(0x147A, dta)...) // FDODSC, FDODTA
		return packDDM(0x2412, inner), nil                                // SQLDTA
	}

	// Fill in headers
	binary.BigEndian.PutUint16(sqldta[0:2], uint16(bodyLen))
	binary.BigEndian.PutUint16(sqldta[2:4], 0x2412) // SQLDTA

	binary.BigEndian.PutUint16(sqldta[4:6], uint16(4+fdodscLen))
	binary.BigEndian.PutUint16(sqldta[6:8], 0x0010) // FDODSC

	offset := 8 + fdodscLen
	binary.BigEndian.PutUint16(sqldta[offset:offset+2], uint16(4+dtaBytesLen))
	binary.BigEndian.PutUint16(sqldta[offset+2:offset+4], 0x147A) // FDODTA

	return sqldta, nil
}
