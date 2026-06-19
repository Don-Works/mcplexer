package triggers

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/scheduler"
	"github.com/don-works/mcplexer/internal/store"
)

// plistRoot / plistDict / plistNode form the minimal XML shape we care
// about: a flat <dict> with alternating <key>/<value> children.
type plistRoot struct {
	XMLName xml.Name  `xml:"plist"`
	Dict    plistDict `xml:"dict"`
}

type plistDict struct {
	Items []plistNode `xml:",any"`
}

type plistNode struct {
	XMLName xml.Name
	Value   string      `xml:",chardata"`
	Array   []plistNode `xml:",any"`
}

// candidateFromPlist parses one launchd plist into a Candidate. Returns
// (_, false, nil) if the plist has no schedule trigger (we only adopt
// time-driven agents — long-lived KeepAlive daemons are out of scope).
func (w *ImportWizard) candidateFromPlist(path string) (Candidate, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Candidate{}, false, err
	}
	var root plistRoot
	if err := xml.Unmarshal(data, &root); err != nil {
		return Candidate{}, false, parseErr("parse plist", err)
	}
	kv := flattenPlistDict(root.Dict)
	label := stringValue(kv["Label"])
	cmd, argv := programArguments(kv["ProgramArguments"])
	job := store.ScheduledJob{
		Name:     "launchd-" + label,
		Command:  cmd,
		ArgsJSON: marshalArgv(argv),
		Surface:  "schedule",
		Enabled:  true,
	}
	if iv := stringValue(kv["StartInterval"]); iv != "" {
		job.Kind = scheduler.KindInterval
		job.Spec = (time.Duration(parseIntDefault(iv, 0)) * time.Second).String()
	} else if cal, ok := kv["StartCalendarInterval"]; ok {
		job.Kind = scheduler.KindCron
		job.Spec = calendarIntervalToCron(cal)
	} else {
		return Candidate{}, false, nil
	}
	return Candidate{
		Job:    job,
		Source: ImportSource{Kind: "launchd", Path: label, Excerpt: string(data)},
	}, true, nil
}

// flattenPlistDict turns the alternating <key>/<value> list into a map
// keyed by the preceding <key> element's text.
func flattenPlistDict(d plistDict) map[string]plistNode {
	out := map[string]plistNode{}
	var lastKey string
	for _, n := range d.Items {
		if n.XMLName.Local == "key" {
			lastKey = strings.TrimSpace(n.Value)
			continue
		}
		if lastKey != "" {
			out[lastKey] = n
			lastKey = ""
		}
	}
	return out
}

func stringValue(n plistNode) string {
	if n.XMLName.Local == "true" {
		return "true"
	}
	if n.XMLName.Local == "false" {
		return "false"
	}
	return strings.TrimSpace(n.Value)
}

// programArguments lifts the (cmd, argv) pair from a <array> of
// <string> entries.
func programArguments(n plistNode) (string, []string) {
	if n.XMLName.Local != "array" {
		return "", nil
	}
	var argv []string
	for _, child := range n.Array {
		if child.XMLName.Local == "string" {
			argv = append(argv, strings.TrimSpace(child.Value))
		}
	}
	if len(argv) == 0 {
		return "", nil
	}
	return argv[0], argv[1:]
}

// calendarIntervalToCron converts a StartCalendarInterval <dict> to a
// 5-field cron expression. Missing fields stay as "*".
func calendarIntervalToCron(n plistNode) string {
	if n.XMLName.Local != "dict" {
		return ""
	}
	fields := map[string]string{
		"Minute": "*", "Hour": "*", "Day": "*", "Month": "*", "Weekday": "*",
	}
	var lastKey string
	for _, c := range n.Array {
		if c.XMLName.Local == "key" {
			lastKey = strings.TrimSpace(c.Value)
			continue
		}
		if lastKey != "" && c.XMLName.Local == "integer" {
			fields[lastKey] = strings.TrimSpace(c.Value)
			lastKey = ""
		}
	}
	return strings.Join([]string{
		fields["Minute"], fields["Hour"], fields["Day"], fields["Month"], fields["Weekday"],
	}, " ")
}

// candidateFromTimer parses a systemd .timer + adjacent .service pair.
func (w *ImportWizard) candidateFromTimer(path string) (Candidate, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Candidate{}, false, err
	}
	ini := parseINI(string(data))
	timer := ini["Timer"]
	cmd, argv := lookupServiceExec(filepath.Dir(path), path, timer["Unit"])
	label := strings.TrimSuffix(filepath.Base(path), ".timer")
	job := store.ScheduledJob{
		Name: "systemd-" + label, Command: cmd, ArgsJSON: marshalArgv(argv),
		Surface: "schedule", Enabled: true,
	}
	src := ImportSource{Kind: "systemd", Path: path, Excerpt: string(data)}
	if oas := timer["OnUnitActiveSec"]; oas != "" {
		job.Kind = scheduler.KindInterval
		job.Spec = normaliseSystemdDuration(oas)
		return Candidate{Job: job, Source: src}, true, nil
	}
	if obs := timer["OnBootSec"]; obs != "" {
		job.Kind = scheduler.KindInterval
		job.Spec = normaliseSystemdDuration(obs)
		return Candidate{Job: job, Source: src}, true, nil
	}
	if onc := timer["OnCalendar"]; onc != "" {
		job.Kind = scheduler.KindCron
		job.Spec = ""
		return Candidate{Job: job, Source: src, Warning: "OnCalendar=" + onc + " — set Spec manually"}, true, nil
	}
	return Candidate{}, false, nil
}

// parseINI is a tiny INI parser sufficient for systemd unit files.
func parseINI(s string) map[string]map[string]string {
	out := map[string]map[string]string{}
	section := ""
	for _, raw := range strings.Split(s, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			if _, ok := out[section]; !ok {
				out[section] = map[string]string{}
			}
			continue
		}
		if section == "" {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		out[section][strings.TrimSpace(line[:eq])] = strings.TrimSpace(line[eq+1:])
	}
	return out
}

// lookupServiceExec resolves the .service file paired with a .timer
// (via Unit= or filename convention) and returns ExecStart split into
// (command, argv). Best-effort.
func lookupServiceExec(dir, timerPath, unitOverride string) (string, []string) {
	base := strings.TrimSuffix(filepath.Base(timerPath), ".timer")
	if unitOverride != "" {
		base = strings.TrimSuffix(unitOverride, ".service")
	}
	data, err := os.ReadFile(filepath.Join(dir, base+".service"))
	if err != nil {
		return "", nil
	}
	ini := parseINI(string(data))
	execLine := ini["Service"]["ExecStart"]
	if execLine == "" {
		return "", nil
	}
	fields := strings.Fields(execLine)
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], fields[1:]
}

// normaliseSystemdDuration tries to convert systemd duration syntax
// ("1h", "30min", "5 s") to a time.Duration string Go can parse.
func normaliseSystemdDuration(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	s = strings.ReplaceAll(s, "min", "m")
	s = strings.ReplaceAll(s, "sec", "s")
	s = strings.ReplaceAll(s, "hr", "h")
	return s
}

func parseIntDefault(s string, dflt int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return dflt
	}
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return dflt
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

// marshalArgv encodes a string slice as the JSON form ScheduledJob.ArgsJSON
// expects. Empty argv -> "[]" (matching the sqlite default).
func marshalArgv(argv []string) string {
	if len(argv) == 0 {
		return "[]"
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, a := range argv {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		for _, r := range a {
			switch r {
			case '\\':
				b.WriteString(`\\`)
			case '"':
				b.WriteString(`\"`)
			default:
				b.WriteRune(r)
			}
		}
		b.WriteByte('"')
	}
	b.WriteByte(']')
	return b.String()
}

// parseErr wraps a parse error so the wizard surfaces "couldn't parse
// this file" without forcing the test to import fmt.
func parseErr(label string, err error) error {
	return &wrappedErr{label: label, inner: err}
}

type wrappedErr struct {
	label string
	inner error
}

func (e *wrappedErr) Error() string { return e.label + ": " + e.inner.Error() }
func (e *wrappedErr) Unwrap() error { return e.inner }
