package iso8601

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/Code-Hex/synchro/internal/constraints"
)

const (
	durationBasicFormat    = "PYYYYMMDDThhmmss"
	durationExtendedFormat = "PYYYY-MM-DDThh:mm:ss"
)

// ParseDuration attempts to parse a given byte slice representing a duration in the
// ISO 8601 format. Supported formats align with the regular expression patterns:
//
//	fraction:     (\d+)(?:[.,](\d{1,9}))?
//	durationTime: (?:${fraction}H)?(?:${fraction}M)?(?:${fraction}S)?
//	durationDate: (?:(\d+)Y)?(?:(\d+)M)?(?:(\d+)W)?(?:(\d+)D)?
//	duration:     ^([+-])?P${durationDate}(?:T(?!$)${durationTime})?$
//
// Examples of valid durations include:
//
//	PnYnMnDTnHnMnS (e.g., P3Y6M4DT12H30M5S)
//	PnW (e.g., P4W)
//
// According to the ISO 8601-1 standard, weeks are not allowed to appear together
// with any other units, and durations can only be positive. However, as extensions
// to the standard, ISO 8601-2 allows a sign character at the start of the string and
// permits combining weeks with other units. If using a string such as P3W1D, +P1M,
// or -P1M for interoperability, be aware that other programs may not recognize it.
//
// The function returns a Duration structure or an error if the parsing fails.
func ParseDuration[bytes constraints.Bytes](b bytes) (Duration, error) {
	if len(b) == len(durationBasicFormat) || len(b) == len(durationExtendedFormat) {
		d, err := parseAlternativeDuration([]byte(b))
		if err == nil {
			return d, nil
		}
		if err, ok := err.(*DurationRangeError); ok {
			return Duration{}, err
		}
	}
	return parseDuration([]byte(b))
}

func parseDuration(b []byte) (Duration, error) {
	var (
		y                 int
		m                 int
		w                 int
		d                 int
		hour              int
		minute            int
		second            int
		excessNanoseconds int
		negative          bool
	)

	if len(b) == 0 {
		return Duration{}, &UnexpectedTokenError{
			Value:      string(b),
			Token:      string(b),
			AfterToken: "",
			Expected:   "P or + or -",
		}
	}

	i := 0
	switch b[0] {
	case '+', '-':
		if b[0] == '-' {
			negative = true
		}
		if len(b[1:]) == 0 {
			return Duration{}, &UnexpectedTokenError{
				Value:      string(b),
				Token:      string(b),
				AfterToken: string(b[0]),
				Expected:   "P",
			}
		}
		i++
		fallthrough
	case 'P':
		i++
	default:
		return Duration{}, &UnexpectedTokenError{
			Value:      string(b),
			Token:      string(b),
			AfterToken: "",
			Expected:   "P or + or -",
		}
	}

	if len(b[i:]) == 0 {
		return Duration{}, &UnexpectedTokenError{
			Value:      string(b),
			Token:      "",
			AfterToken: "P",
			Expected:   "PnYnMnDTnHnMnS or PnW format",
		}
	}

	// Separate because the 'M' designator exists in both date and time.
	// dateSeen keys will be 'Y', 'M', 'D', 'W'
	dateSeen := make(map[byte]bool, 3)
	// timeSeen keys will be 'H', 'M', 'S'
	timeSeen := make(map[byte]bool, 3)

	// Because only the smallest unit can be fractional
	seenFranction := false

	// the 'T'
	seenT := false

	setter := func(idx int, seen map[byte]bool, setter func() error) error {
		if seen[b[idx]] {
			return &UnexpectedTokenError{
				Value:      string(b),
				Token:      string(b[idx]),
				AfterToken: string(b[:idx]),
				Expected:   "the designator to be used only once",
			}
		}
		if err := setter(); err != nil {
			return err
		}
		seen[b[idx]] = true
		return nil
	}
	dateSetter := func(idx int, f func() error) error {
		return setter(idx, dateSeen, func() error {
			if seenT {
				return &UnexpectedTokenError{
					Value:      string(b),
					Token:      string(b[idx]),
					AfterToken: string(b[:idx]),
					Expected:   "date duration should put before the 'T'",
				}
			}
			return f()
		})
	}
	timeSetter := func(idx int, f func() error) error {
		return setter(idx, timeSeen, func() error {
			if !seenT {
				return &UnexpectedTokenError{
					Value:      string(b),
					Token:      string(b[idx]),
					AfterToken: string(b[:idx]),
					Expected:   "the 'T' designator is required",
				}
			}
			return f()
		})
	}

	for i < len(b) {
		n := countDigits(b[i:], 0)
		if n == 0 {
			if b[i] == 'T' {
				if seenT {
					return Duration{}, &UnexpectedTokenError{
						Value:      string(b),
						Token:      "T",
						AfterToken: string(b[:i]),
						Expected:   "the 'T' designator should be once",
					}
				}
				i++
				seenT = true
				continue
			}
			return Duration{}, &UnexpectedTokenError{
				Value:      string(b),
				Token:      string(b[i]),
				AfterToken: string(b[:i]),
				Expected:   "PnYnMnDTnHnMnS or PnW format",
			}
		}

		// Parse the number
		val := parseNumber(b, i, n)
		i += n

		// check fraction
		fraction := 0
		if i < len(b) && (b[i] == '.' || b[i] == ',') {
			if !seenT {
				return Duration{}, &UnexpectedTokenError{
					Value:      string(b),
					Token:      string(b[i]),
					AfterToken: string(b[:i]),
					Expected:   "only the time unit can be fractional",
				}
			}
			if seenFranction {
				return Duration{}, &UnexpectedTokenError{
					Value:      string(b),
					Token:      string(b[i]),
					AfterToken: string(b[:i]),
					Expected:   "only the smallest time unit can be fractional",
				}
			}
			i++
			var digits int
			fraction, digits = parseFraction(b[i:])
			i += digits
			seenFranction = true
		}

		// Decode based on the current suffix
		if i < len(b) {
			switch b[i] {
			case 'Y':
				err := dateSetter(i, func() error {
					for _, designator := range []byte{'M', 'W', 'D'} {
						if dateSeen[designator] {
							return &UnexpectedTokenError{
								Value:      string(b),
								Token:      "Y",
								AfterToken: string(b[:i]),
								Expected:   fmt.Sprintf("the 'Y' date designator should appear before '%s'", string(designator)),
							}
						}
					}
					y = val
					return nil
				})
				if err != nil {
					return Duration{}, err
				}
			case 'M':
				if seenT {
					// minute
					err := timeSetter(i, func() error {
						if timeSeen['S'] {
							return &UnexpectedTokenError{
								Value:      string(b),
								Token:      "M",
								AfterToken: string(b[:i]),
								Expected:   "the 'M' time designator should appear before 'S'",
							}
						}
						minute = val
						return nil
					})
					if err != nil {
						return Duration{}, err
					}
					if fraction > 0 {
						excessNanoseconds = fraction * 60
					}
				} else {
					// month
					err := dateSetter(i, func() error {
						for _, designator := range []byte{'W', 'D'} {
							if dateSeen[designator] {
								return &UnexpectedTokenError{
									Value:      string(b),
									Token:      "M",
									AfterToken: string(b[:i]),
									Expected:   fmt.Sprintf("the 'M' date designator should appear before '%s'", string(designator)),
								}
							}
						}
						m = val
						return nil
					})
					if err != nil {
						return Duration{}, err
					}
				}
			case 'W':
				err := dateSetter(i, func() error {
					if dateSeen['D'] {
						return &UnexpectedTokenError{
							Value:      string(b),
							Token:      "W",
							AfterToken: string(b[:i]),
							Expected:   "the 'W' date designator should appear before 'D'",
						}
					}
					w = val
					return nil
				})
				if err != nil {
					return Duration{}, err
				}
			case 'D':
				err := dateSetter(i, func() error {
					d = val
					return nil
				})
				if err != nil {
					return Duration{}, err
				}
			case 'H':
				err := timeSetter(i, func() error {
					for _, designator := range []byte{'M', 'S'} {
						if timeSeen[designator] {
							return &UnexpectedTokenError{
								Value:      string(b),
								Token:      "H",
								AfterToken: string(b[:i]),
								Expected:   fmt.Sprintf("the 'H' time designator should appear before '%s'", string(designator)),
							}
						}
					}
					hour = val
					return nil
				})
				if err != nil {
					return Duration{}, err
				}
				if fraction > 0 {
					excessNanoseconds = fraction * 3600
				}
			case 'S':
				err := timeSetter(i, func() error {
					second = val
					return nil
				})
				if err != nil {
					return Duration{}, err
				}
				if fraction > 0 {
					excessNanoseconds = fraction
				}
			default:
				return Duration{}, &UnexpectedTokenError{
					Value:      string(b),
					Token:      string(b[i]),
					AfterToken: string(b[:i]),
					Expected:   "PnYnMnDTnHnMnS or PnW format",
				}
			}
			i++
		}
	}

	// https://github.com/tc39/proposal-temporal/blob/4a5e88e2334c8ab428590bfd20a706d1bb6c6031/polyfill/lib/ecmascript.mjs#L635
	nanosec := excessNanoseconds % 1000
	microsec := int(math.Trunc(float64(excessNanoseconds)/1000)) % 1000
	millisec := int(math.Trunc(float64(excessNanoseconds)/1e6)) % 1000
	second += int(math.Trunc(float64(excessNanoseconds)/1e9)) % 60
	minute += int(math.Trunc(float64(excessNanoseconds) / 60e9))

	return Duration{
		Year:        y,
		Month:       time.Month(m),
		Week:        w,
		Day:         d,
		Hour:        hour,
		Minute:      minute,
		Second:      second,
		Millisecond: millisec,
		Microsecond: microsec,
		Nanosecond:  nanosec,
		Negative:    negative,
	}, nil
}

// len(PYYYYMMDDThhmmss) => 16
// len(PYYYY-MM-DDThh:mm:ss) => 20
func parseAlternativeDuration(b []byte) (Duration, error) {
	if b[0] != 'P' {
		return Duration{}, &UnexpectedTokenError{
			Value:      string(b),
			Token:      string(b[0]),
			AfterToken: "",
			Expected:   "P",
		}
	}
	n := countDigits(b, 1)
	switch n {
	case 8: //  PYYYYMMDDThhmmss
		if b[9] != 'T' {
			return Duration{}, &UnexpectedTokenError{
				Value:      string(b),
				Token:      string(b[9]),
				AfterToken: string(b[:9]),
				Expected:   "T",
			}
		}
		if c := countDigits(b, 10); c != 6 {
			return Duration{}, &UnexpectedTokenError{
				Value:      string(b),
				Token:      humanizeDigits(c),
				AfterToken: "T",
				Expected:   humanizeDigits(6),
			}
		}
		return newAlternativeDuration(
			parseNumber(b, 1, 4),  // YYYY
			parseNumber(b, 5, 2),  // MM
			parseNumber(b, 7, 2),  // DD
			parseNumber(b, 10, 2), // hh
			parseNumber(b, 12, 2), // mm
			parseNumber(b, 14, 2), // ss
		)
	case 4: // PYYYY-MM-DDThh:mm:ss
		if b[5] != '-' {
			return Duration{}, &UnexpectedTokenError{
				Value:      string(b),
				Token:      string(b[5]),
				AfterToken: string(b[:5]),
				Expected:   "-",
			}
		}
		if c := countDigits(b, 6); c != 2 {
			return Duration{}, &UnexpectedTokenError{
				Value:      string(b),
				Token:      humanizeDigits(c),
				AfterToken: "-",
				Expected:   humanizeDigits(2),
			}
		}
		if b[8] != '-' {
			return Duration{}, &UnexpectedTokenError{
				Value:      string(b),
				Token:      string(b[8]),
				AfterToken: string(b[:8]),
				Expected:   "-",
			}
		}
		if c := countDigits(b, 9); c != 2 {
			return Duration{}, &UnexpectedTokenError{
				Value:      string(b),
				Token:      humanizeDigits(c),
				AfterToken: "-",
				Expected:   humanizeDigits(2),
			}
		}
		if b[11] != 'T' {
			return Duration{}, &UnexpectedTokenError{
				Value:      string(b),
				Token:      string(b[11]),
				AfterToken: string(b[:11]),
				Expected:   "T",
			}
		}
		if c := countDigits(b, 12); c != 2 {
			return Duration{}, &UnexpectedTokenError{
				Value:      string(b),
				Token:      humanizeDigits(c),
				AfterToken: "T",
				Expected:   humanizeDigits(2),
			}
		}
		if b[14] != ':' {
			return Duration{}, &UnexpectedTokenError{
				Value:      string(b),
				Token:      string(b[14]),
				AfterToken: string(b[:14]),
				Expected:   ":",
			}
		}
		if c := countDigits(b, 15); c != 2 {
			return Duration{}, &UnexpectedTokenError{
				Value:      string(b),
				Token:      humanizeDigits(c),
				AfterToken: ":",
				Expected:   humanizeDigits(2),
			}
		}
		if b[17] != ':' {
			return Duration{}, &UnexpectedTokenError{
				Value:      string(b),
				Token:      string(b[17]),
				AfterToken: string(b[:17]),
				Expected:   ":",
			}
		}
		if c := countDigits(b, 18); c != 2 {
			return Duration{}, &UnexpectedTokenError{
				Value:      string(b),
				Token:      humanizeDigits(c),
				AfterToken: ":",
				Expected:   humanizeDigits(2),
			}
		}
		return newAlternativeDuration(
			parseNumber(b, 1, 4),  // YYYY
			parseNumber(b, 6, 2),  // MM
			parseNumber(b, 9, 2),  // DD
			parseNumber(b, 12, 2), // hh
			parseNumber(b, 15, 2), // mm
			parseNumber(b, 18, 2), // ss
		)
	}
	return Duration{}, &UnexpectedTokenError{
		Value:      string(b),
		Token:      humanizeDigits(n),
		AfterToken: "P",
		Expected:   fmt.Sprintf("%s or %s", humanizeDigits(4), humanizeDigits(8)),
	}
}

// > However, individual date and time values cannot exceed their moduli
// (e.g. a value of 13 for the month or 25 for the hour would not be permissible).
//
// See: https://en.wikipedia.org/wiki/ISO_8601
func newAlternativeDuration(
	year int,
	month int,
	day int,
	hour int,
	minute int,
	second int,
) (Duration, error) {
	if month < 0 || month > 12 {
		return Duration{}, &DurationRangeError{
			Element: "month",
			Value:   month,
			Min:     0,
			Max:     12,
		}
	}
	if day < 0 || day > 31 {
		return Duration{}, &DurationRangeError{
			Element: "day",
			Value:   day,
			Min:     0,
			Max:     31,
		}
	}
	if minute < 0 || minute > 59 {
		return Duration{}, &DurationRangeError{
			Element: "minute",
			Value:   minute,
			Min:     0,
			Max:     59,
		}
	}
	if second < 0 || second > 59 {
		return Duration{}, &DurationRangeError{
			Element: "second",
			Value:   second,
			Min:     0,
			Max:     59,
		}
	}
	if hour < 0 || hour > 23 {
		if !(hour == 24 && minute == 0 && second == 0) {
			return Duration{}, &DurationRangeError{
				Element: "hour",
				Value:   hour,
				Min:     0,
				Max:     24,
			}
		}
	}
	return Duration{
		Year:   year,
		Month:  time.Month(month),
		Day:    day,
		Hour:   hour,
		Minute: minute,
		Second: second,
	}, nil
}

// Duration represents an ISO8601 duration with the maximum precision of nanoseconds.
// It includes components like years, months, weeks, days, hours, minutes, seconds,
// milliseconds, microseconds, and nanoseconds. The Negative field indicates whether
// the duration is negative.
type Duration struct {
	Year        int
	Month       time.Month
	Week        int
	Day         int
	Hour        int
	Minute      int
	Second      int
	Millisecond int
	Microsecond int
	Nanosecond  int
	Negative    bool
}

const yearInSecond = 31556952 * time.Second // 365.2425 days * 3600 * 24 seconds
const monthInSecond = 2630016 * time.Second // 30.44 days * 3600 * 24 seconds
const weekInSecond = 7 * dayInSecond
const dayInSecond = 24 * 3600 * time.Second

// StdClockDuration converts an ISO8601 Duration to a standard Go time.Duration.
// The conversion is calculated using hours, minutes, seconds, milliseconds, microseconds and nanoseconds.
func (d Duration) StdClockDuration() time.Duration {
	duration := time.Duration(d.Hour)*time.Hour +
		time.Duration(d.Minute)*time.Minute +
		time.Duration(d.Second)*time.Second +
		time.Duration(d.Millisecond)*time.Millisecond +
		time.Duration(d.Microsecond)*time.Microsecond +
		time.Duration(d.Nanosecond)
	if d.Negative {
		duration = -duration
	}
	return time.Duration(duration)
}

// StdDuration converts the ISO8601 Duration to a standard Go time.Duration.
// Note: This conversion is an approximation. The duration of some components
// like years and months are averaged based on typical values:
//   - 1 year is considered as 365.2425 days (or 31556952 seconds).
//   - 1 month is considered as 30.44 days (or 2630016 seconds).
//   - 1 week is considered as 7 days (or 604800 seconds).
func (d Duration) StdDuration() time.Duration {
	duration := time.Duration(d.Year)*yearInSecond +
		time.Duration(d.Month)*monthInSecond +
		time.Duration(d.Week)*weekInSecond +
		time.Duration(d.Day)*dayInSecond +
		time.Duration(d.Hour)*time.Hour +
		time.Duration(d.Minute)*time.Minute +
		time.Duration(d.Second)*time.Second +
		time.Duration(d.Millisecond)*time.Millisecond +
		time.Duration(d.Microsecond)*time.Microsecond +
		time.Duration(d.Nanosecond)

	if d.Negative {
		duration = -duration
	}
	return time.Duration(duration)
}

// NewDuration makes ISO8601 Duration struct from time.Duration.
func NewDuration(d time.Duration) Duration {
	negative := false
	if d < 0 {
		negative = true
		d = -d
	}

	year := int(d / yearInSecond)
	d -= time.Duration(year) * yearInSecond

	month := int(d / monthInSecond)
	d -= time.Duration(month) * monthInSecond

	week := int(d / weekInSecond)
	d -= time.Duration(week) * weekInSecond

	day := int(d / dayInSecond)
	d -= time.Duration(day) * dayInSecond

	hour := int(d / time.Hour)
	d -= time.Duration(hour) * time.Hour

	minute := int(d / time.Minute)
	d -= time.Duration(minute) * time.Minute

	second := int(d / time.Second)
	d -= time.Duration(second) * time.Second

	millisecond := int(d / time.Millisecond)
	d -= time.Duration(millisecond) * time.Millisecond

	microsecond := int(d / time.Microsecond)
	d -= time.Duration(microsecond) * time.Microsecond

	nanosecond := int(d)

	return Duration{
		Year:        year,
		Month:       time.Month(month),
		Week:        week,
		Day:         day,
		Hour:        hour,
		Minute:      minute,
		Second:      second,
		Millisecond: millisecond,
		Microsecond: microsecond,
		Nanosecond:  nanosecond,
		Negative:    negative,
	}
}

// Negate changes the sign of the duration.
func (d Duration) Negate() Duration {
	src := &d
	dst := &Duration{}
	*dst = *src
	dst.Negative = !dst.Negative
	return *dst
}

// IsZero checks duration is zero value.
func (d Duration) IsZero() bool {
	return d == Duration{} || d == Duration{Negative: true}
}

// String returns the ISO8601 string representation of the duration.
// For example: "P1Y2M3DT4H5M6.007S".
func (d Duration) String() string {
	var b strings.Builder
	if d.Negative {
		b.WriteByte('-')
	}
	b.WriteByte('P')
	if d.IsZero() {
		b.WriteString("T0S")
		return b.String()
	}

	hasTime := false
	write := func(v int, designator byte, isTime bool) {
		if !hasTime && isTime {
			b.WriteByte('T')
			hasTime = true
		}
		b.WriteString(strconv.Itoa(v))
		b.WriteByte(designator)
	}

	writeSec := func(v int, fraction int) {
		if !hasTime && (fraction > 0 || d.Second > 0) {
			b.WriteByte('T')
			hasTime = true
		}
		if fraction > 0 {
			b.WriteString(strconv.Itoa(v))
			b.WriteByte('.')
			fmt.Fprintf(&b, "%09d", fraction)
			b.WriteByte('S')
		} else if d.Second > 0 {
			b.WriteString(strconv.Itoa(v))
			b.WriteByte('S')
		}
	}

	if d.Year != 0 {
		write(d.Year, 'Y', false)
	}
	if d.Month != 0 {
		write(int(d.Month), 'M', false)
	}
	if d.Week != 0 {
		write(d.Week, 'W', false)
	}
	if d.Day != 0 {
		write(d.Day, 'D', false)
	}
	if d.Hour != 0 {
		write(d.Hour, 'H', true)
	}
	if d.Minute != 0 {
		write(d.Minute, 'M', true)
	}

	nanosec := 0
	if d.Millisecond != 0 {
		nanosec += d.Millisecond * 1e6
	}
	if d.Microsecond != 0 {
		nanosec += d.Microsecond * 1000
	}
	if d.Nanosecond != 0 {
		nanosec += d.Nanosecond
	}

	writeSec(d.Second, nanosec)
	return b.String()
}

// DurationRangeError indicates that a value is not in an expected range for Duration.
type DurationRangeError struct {
	Element string
	Value   int
	Min     int
	Max     int
}

// Error implements the error interface.
func (e *DurationRangeError) Error() string {
	return fmt.Sprintf("iso8601 duration: %d %s is not in range %d-%d", e.Value, e.Element, e.Min, e.Max)
}
