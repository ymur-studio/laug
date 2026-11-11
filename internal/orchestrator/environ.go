package orchestrator

import "strings"

// getenv returns key's value in environ, one KEY=value a string, or "" when it is absent: the first entry wins, as
// with os.Getenv. The roles laug runs in its own process (WorkerMain, StarterMain, ProbeMain and RecordMain) read the
// environment laug built for them this way, in place of the process's.
func getenv(environ []string, key string) string {
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// laugWord is the word a line the roles print for a person to type starts with: LAUG in environ, which laug sets for
// each role it runs, laug when its PATH finds one and bin/laug when not; laug when environ holds none, as only a
// test's does.
func laugWord(environ []string) string {
	if w := getenv(environ, "LAUG"); w != "" {
		return w
	}
	return "laug"
}
