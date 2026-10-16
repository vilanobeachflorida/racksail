package i18n

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// conventions are how a language writes dates, times and numbers, from the
// Unicode CLDR. Patterns use CLDR's letters: y year, M month, d day, E
// weekday, H and h hour, m minute, s second, a the period of the day and z
// the time zone, with literal text in single quotes.
type conventions struct {
	// dates and times are the patterns of the full, long, medium and short
	// styles, in that order.
	dates, times [4]string
	// months and days are the abbreviated and wide names, days from Sunday.
	months [2][12]string
	days   [2][7]string
	am, pm string
	// decimal and group separate a number's fraction and its thousands;
	// minGroup is how many digits a number needs above its first group
	// before it is grouped, 2 for languages that write 1234 but 12.345.
	decimal, group string
	minGroup       int
	// percent is what follows a percentage.
	percent string
}

// Styles of date and time arguments, as ICU names them.
var styles = map[string]int{"full": 0, "long": 1, "medium": 2, "short": 3}

var englishMonths = [2][12]string{
	{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"},
	{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"},
}

var englishDays = [2][7]string{
	{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"},
	{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"},
}

// twentyFour are the time patterns of languages that write a 24-hour clock
// with leading zeros.
var twentyFour = [4]string{"HH:mm:ss zzzz", "HH:mm:ss z", "HH:mm:ss", "HH:mm"}

// root is CLDR's root locale, for languages without conventions here:
// ISO 8601 dates and a 24-hour clock, which read the same in any language.
var root = &conventions{
	dates:  [4]string{"y-MM-dd", "y-MM-dd", "y-MM-dd", "y-MM-dd"},
	times:  [4]string{"HH:mm:ss z", "HH:mm:ss z", "HH:mm:ss", "HH:mm"},
	months: englishMonths, days: englishDays, am: "AM", pm: "PM",
	decimal: ".", group: ",", minGroup: 1, percent: "%",
}

var localeConventions = map[Locale]*conventions{
	"en": {
		dates:  [4]string{"EEEE, MMMM d, y", "MMMM d, y", "MMM d, y", "M/d/yy"},
		times:  [4]string{"h:mm:ss a zzzz", "h:mm:ss a z", "h:mm:ss a", "h:mm a"},
		months: englishMonths, days: englishDays, am: "AM", pm: "PM",
		decimal: ".", group: ",", minGroup: 1, percent: "%",
	},
	"en-gb": {
		dates: [4]string{"EEEE d MMMM y", "d MMMM y", "d MMM y", "dd/MM/y"},
		times: twentyFour,
		months: [2][12]string{
			{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sept", "Oct", "Nov", "Dec"},
			englishMonths[1],
		},
		days: englishDays, am: "am", pm: "pm",
		decimal: ".", group: ",", minGroup: 1, percent: "%",
	},
	"de": {
		dates: [4]string{"EEEE, d. MMMM y", "d. MMMM y", "dd.MM.y", "dd.MM.yy"},
		times: twentyFour,
		months: [2][12]string{
			{"Jan.", "Feb.", "März", "Apr.", "Mai", "Juni", "Juli", "Aug.", "Sept.", "Okt.", "Nov.", "Dez."},
			{"Januar", "Februar", "März", "April", "Mai", "Juni", "Juli", "August", "September", "Oktober", "November", "Dezember"},
		},
		days: [2][7]string{
			{"So.", "Mo.", "Di.", "Mi.", "Do.", "Fr.", "Sa."},
			{"Sonntag", "Montag", "Dienstag", "Mittwoch", "Donnerstag", "Freitag", "Samstag"},
		},
		am: "AM", pm: "PM",
		decimal: ",", group: ".", minGroup: 1, percent: " %",
	},
	"fr": {
		dates: [4]string{"EEEE d MMMM y", "d MMMM y", "d MMM y", "dd/MM/y"},
		times: twentyFour,
		months: [2][12]string{
			{"janv.", "févr.", "mars", "avr.", "mai", "juin", "juil.", "août", "sept.", "oct.", "nov.", "déc."},
			{"janvier", "février", "mars", "avril", "mai", "juin", "juillet", "août", "septembre", "octobre", "novembre", "décembre"},
		},
		days: [2][7]string{
			{"dim.", "lun.", "mar.", "mer.", "jeu.", "ven.", "sam."},
			{"dimanche", "lundi", "mardi", "mercredi", "jeudi", "vendredi", "samedi"},
		},
		am: "AM", pm: "PM",
		decimal: ",", group: " ", minGroup: 1, percent: " %",
	},
	"es": {
		dates: [4]string{"EEEE, d 'de' MMMM 'de' y", "d 'de' MMMM 'de' y", "d MMM y", "d/M/yy"},
		times: [4]string{"H:mm:ss (zzzz)", "H:mm:ss z", "H:mm:ss", "H:mm"},
		months: [2][12]string{
			{"ene", "feb", "mar", "abr", "may", "jun", "jul", "ago", "sept", "oct", "nov", "dic"},
			{"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre"},
		},
		days: [2][7]string{
			{"dom", "lun", "mar", "mié", "jue", "vie", "sáb"},
			{"domingo", "lunes", "martes", "miércoles", "jueves", "viernes", "sábado"},
		},
		am: "a. m.", pm: "p. m.",
		decimal: ",", group: ".", minGroup: 2, percent: " %",
	},
	"it": {
		dates: [4]string{"EEEE d MMMM y", "d MMMM y", "d MMM y", "dd/MM/yy"},
		times: twentyFour,
		months: [2][12]string{
			{"gen", "feb", "mar", "apr", "mag", "giu", "lug", "ago", "set", "ott", "nov", "dic"},
			{"gennaio", "febbraio", "marzo", "aprile", "maggio", "giugno", "luglio", "agosto", "settembre", "ottobre", "novembre", "dicembre"},
		},
		days: [2][7]string{
			{"dom", "lun", "mar", "mer", "gio", "ven", "sab"},
			{"domenica", "lunedì", "martedì", "mercoledì", "giovedì", "venerdì", "sabato"},
		},
		am: "AM", pm: "PM",
		decimal: ",", group: ".", minGroup: 1, percent: "%",
	},
	"pt": {
		dates: [4]string{"EEEE, d 'de' MMMM 'de' y", "d 'de' MMMM 'de' y", "d 'de' MMM 'de' y", "dd/MM/y"},
		times: twentyFour,
		months: [2][12]string{
			{"jan.", "fev.", "mar.", "abr.", "mai.", "jun.", "jul.", "ago.", "set.", "out.", "nov.", "dez."},
			{"janeiro", "fevereiro", "março", "abril", "maio", "junho", "julho", "agosto", "setembro", "outubro", "novembro", "dezembro"},
		},
		days: [2][7]string{
			{"dom.", "seg.", "ter.", "qua.", "qui.", "sex.", "sáb."},
			{"domingo", "segunda-feira", "terça-feira", "quarta-feira", "quinta-feira", "sexta-feira", "sábado"},
		},
		am: "AM", pm: "PM",
		decimal: ",", group: ".", minGroup: 1, percent: "%",
	},
}

// conventionsOf returns how a language writes dates and numbers: its
// region's way, its language's, or CLDR's root.
func conventionsOf(l Locale) *conventions {
	if c, ok := localeConventions[Locale(strings.ToLower(string(l)))]; ok {
		return c
	}
	if c, ok := localeConventions[l.Base()]; ok {
		return c
	}
	return root
}

// typed formats the inside of a date, time or number argument, such as
// "when, date, medium" or "size, number". ok is false when it isn't one,
// or params holds no value of the right type for it.
func typed(inner string, params map[string]any, c *conventions) (string, bool) {
	parts := strings.Split(inner, ",")
	if len(parts) < 2 || len(parts) > 3 {
		return "", false
	}
	name, kind, style := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), ""
	if len(parts) == 3 {
		style = strings.TrimSpace(parts[2])
	}
	v, ok := params[name]
	if !ok || !isName(name) {
		return "", false
	}
	switch kind {
	case "date", "time":
		t, isTime := v.(time.Time)
		if !isTime {
			if p, isPointer := v.(*time.Time); isPointer && p != nil {
				t, isTime = *p, true
			}
		}
		i, known := styles[style]
		if style == "" {
			i, known = styles["medium"], true
		}
		if !isTime || !known {
			return "", false
		}
		if kind == "date" {
			return formatTime(c.dates[i], t, c), true
		}
		return formatTime(c.times[i], t, c), true
	case "number":
		return formatNumber(v, style, c)
	}
	return "", false
}

// formatTime writes t with a CLDR pattern.
func formatTime(pattern string, t time.Time, c *conventions) string {
	var b strings.Builder
	for i := 0; i < len(pattern); {
		ch := pattern[i]
		switch {
		case ch == '\'':
			// Quoted text, where '' stands for a quote.
			if i+1 < len(pattern) && pattern[i+1] == '\'' {
				b.WriteByte('\'')
				i += 2
				continue
			}
			j := i + 1
			for j < len(pattern) {
				if pattern[j] == '\'' {
					if j+1 < len(pattern) && pattern[j+1] == '\'' {
						b.WriteByte('\'')
						j += 2
						continue
					}
					break
				}
				b.WriteByte(pattern[j])
				j++
			}
			i = j + 1
		case ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z':
			n := 1
			for i+n < len(pattern) && pattern[i+n] == ch {
				n++
			}
			b.WriteString(timeField(ch, n, t, c))
			i += n
		default:
			r, size := utf8.DecodeRuneInString(pattern[i:])
			b.WriteRune(r)
			i += size
		}
	}
	return b.String()
}

// timeField writes one field of a pattern, a letter repeated n times.
func timeField(ch byte, n int, t time.Time, c *conventions) string {
	pad := func(v, width int) string {
		s := strconv.Itoa(v)
		for len(s) < width {
			s = "0" + s
		}
		return s
	}
	switch ch {
	case 'y':
		if n == 2 {
			return pad(t.Year()%100, 2)
		}
		return pad(t.Year(), n)
	case 'M', 'L':
		switch {
		case n >= 4:
			return c.months[1][t.Month()-1]
		case n == 3:
			return c.months[0][t.Month()-1]
		}
		return pad(int(t.Month()), n)
	case 'd':
		return pad(t.Day(), n)
	case 'E':
		if n >= 4 {
			return c.days[1][t.Weekday()]
		}
		return c.days[0][t.Weekday()]
	case 'H':
		return pad(t.Hour(), n)
	case 'h':
		h := t.Hour() % 12
		if h == 0 {
			h = 12
		}
		return pad(h, n)
	case 'm':
		return pad(t.Minute(), n)
	case 's':
		return pad(t.Second(), n)
	case 'a':
		if t.Hour() < 12 {
			return c.am
		}
		return c.pm
	case 'z', 'v', 'Z', 'O':
		name, offset := t.Zone()
		switch {
		case offset == 0 && (name == "" || name == "UTC" || name == "GMT"):
			return "UTC"
		case name != "" && !strings.ContainsAny(name, "+-"):
			return name
		}
		return t.Format("GMT-07:00")
	}
	return strings.Repeat(string(ch), n)
}

// formatNumber writes a number as the language does: grouped and with its
// decimal separator, rounded to a whole number with the integer style, and
// as a percentage with percent. ok is false for a value that isn't a
// number or a style ICU doesn't have.
func formatNumber(v any, style string, c *conventions) (string, bool) {
	var f float64
	var whole bool
	var digits string
	switch x := v.(type) {
	case int:
		f, whole, digits = float64(x), true, strconv.FormatInt(int64(x), 10)
	case int8:
		f, whole, digits = float64(x), true, strconv.FormatInt(int64(x), 10)
	case int16:
		f, whole, digits = float64(x), true, strconv.FormatInt(int64(x), 10)
	case int32:
		f, whole, digits = float64(x), true, strconv.FormatInt(int64(x), 10)
	case int64:
		f, whole, digits = float64(x), true, strconv.FormatInt(x, 10)
	case uint:
		f, whole, digits = float64(x), true, strconv.FormatUint(uint64(x), 10)
	case uint8:
		f, whole, digits = float64(x), true, strconv.FormatUint(uint64(x), 10)
	case uint16:
		f, whole, digits = float64(x), true, strconv.FormatUint(uint64(x), 10)
	case uint32:
		f, whole, digits = float64(x), true, strconv.FormatUint(uint64(x), 10)
	case uint64:
		f, whole, digits = float64(x), true, strconv.FormatUint(x, 10)
	case float32:
		f = float64(x)
	case float64:
		f = x
	default:
		return "", false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", false
	}
	suffix := ""
	switch style {
	case "":
		// ICU's decimal format keeps up to three fraction digits.
		if !whole {
			digits = strings.TrimRight(strings.TrimRight(strconv.FormatFloat(f, 'f', 3, 64), "0"), ".")
		}
	case "integer":
		if !whole {
			digits = strconv.FormatFloat(math.RoundToEven(f), 'f', 0, 64)
		}
	case "percent":
		digits, suffix = strconv.FormatFloat(math.RoundToEven(f*100), 'f', 0, 64), c.percent
	default:
		return "", false
	}
	if digits == "-0" {
		digits = "0"
	}
	return localDigits(digits, c) + suffix, true
}

// localDigits writes a number given as plain digits, with a minus sign and
// a decimal point, with the language's separators.
func localDigits(digits string, c *conventions) string {
	sign := ""
	if strings.HasPrefix(digits, "-") {
		sign, digits = "-", digits[1:]
	}
	integer, fraction, hasFraction := strings.Cut(digits, ".")
	if len(integer) >= 3+c.minGroup {
		var b strings.Builder
		first := len(integer) % 3
		if first == 0 {
			first = 3
		}
		b.WriteString(integer[:first])
		for i := first; i < len(integer); i += 3 {
			b.WriteString(c.group)
			b.WriteString(integer[i : i+3])
		}
		integer = b.String()
	}
	if hasFraction {
		return sign + integer + c.decimal + fraction
	}
	return sign + integer
}

// countText writes the count a plural shows where # is: as the language
// writes numbers when it is one, as it is otherwise.
func countText(v any, c *conventions) string {
	if text, ok := formatNumber(v, "", c); ok {
		return text
	}
	return fmt.Sprint(v)
}
