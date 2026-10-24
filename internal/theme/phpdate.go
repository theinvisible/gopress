package theme

import (
	"fmt"
	"strings"
	"time"
)

// FormatPHPDate formats t with a PHP date() format string, as used by the
// WordPress date_format/time_format options. Escaped characters (\x) are
// copied literally.
func FormatPHPDate(format string, t time.Time) string {
	var b strings.Builder
	for i := 0; i < len(format); i++ {
		c := format[i]
		switch c {
		case '\\':
			if i+1 < len(format) {
				i++
				b.WriteByte(format[i])
			}
		case 'd':
			fmt.Fprintf(&b, "%02d", t.Day())
		case 'D':
			b.WriteString(t.Weekday().String()[:3])
		case 'j':
			fmt.Fprintf(&b, "%d", t.Day())
		case 'l':
			b.WriteString(t.Weekday().String())
		case 'N':
			wd := int(t.Weekday())
			if wd == 0 {
				wd = 7
			}
			fmt.Fprintf(&b, "%d", wd)
		case 'S':
			b.WriteString(ordinalSuffix(t.Day()))
		case 'w':
			fmt.Fprintf(&b, "%d", int(t.Weekday()))
		case 'z':
			fmt.Fprintf(&b, "%d", t.YearDay()-1)
		case 'W':
			_, w := t.ISOWeek()
			fmt.Fprintf(&b, "%02d", w)
		case 'F':
			b.WriteString(t.Month().String())
		case 'M':
			b.WriteString(t.Month().String()[:3])
		case 'm':
			fmt.Fprintf(&b, "%02d", int(t.Month()))
		case 'n':
			fmt.Fprintf(&b, "%d", int(t.Month()))
		case 't':
			fmt.Fprintf(&b, "%d", time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, t.Location()).Day())
		case 'L':
			y := t.Year()
			if y%4 == 0 && (y%100 != 0 || y%400 == 0) {
				b.WriteByte('1')
			} else {
				b.WriteByte('0')
			}
		case 'o':
			y, _ := t.ISOWeek()
			fmt.Fprintf(&b, "%d", y)
		case 'Y':
			fmt.Fprintf(&b, "%04d", t.Year())
		case 'y':
			fmt.Fprintf(&b, "%02d", t.Year()%100)
		case 'a':
			b.WriteString(strings.ToLower(t.Format("PM")))
		case 'A':
			b.WriteString(t.Format("PM"))
		case 'g':
			fmt.Fprintf(&b, "%d", hour12(t))
		case 'G':
			fmt.Fprintf(&b, "%d", t.Hour())
		case 'h':
			fmt.Fprintf(&b, "%02d", hour12(t))
		case 'H':
			fmt.Fprintf(&b, "%02d", t.Hour())
		case 'i':
			fmt.Fprintf(&b, "%02d", t.Minute())
		case 's':
			fmt.Fprintf(&b, "%02d", t.Second())
		case 'u':
			fmt.Fprintf(&b, "%06d", t.Nanosecond()/1000)
		case 'v':
			fmt.Fprintf(&b, "%03d", t.Nanosecond()/1e6)
		case 'e':
			b.WriteString(t.Location().String())
		case 'T':
			b.WriteString(t.Format("MST"))
		case 'P':
			b.WriteString(t.Format("-07:00"))
		case 'p':
			if _, off := t.Zone(); off == 0 {
				b.WriteByte('Z')
			} else {
				b.WriteString(t.Format("-07:00"))
			}
		case 'O':
			b.WriteString(t.Format("-0700"))
		case 'Z':
			_, off := t.Zone()
			fmt.Fprintf(&b, "%d", off)
		case 'c':
			b.WriteString(t.Format("2006-01-02T15:04:05-07:00"))
		case 'r':
			b.WriteString(t.Format("Mon, 02 Jan 2006 15:04:05 -0700"))
		case 'U':
			fmt.Fprintf(&b, "%d", t.Unix())
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func hour12(t time.Time) int {
	h := t.Hour() % 12
	if h == 0 {
		h = 12
	}
	return h
}

func ordinalSuffix(d int) string {
	if d%100 >= 11 && d%100 <= 13 {
		return "th"
	}
	switch d % 10 {
	case 1:
		return "st"
	case 2:
		return "nd"
	case 3:
		return "rd"
	}
	return "th"
}
