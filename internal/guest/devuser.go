package guest

import (
	"os"
	"strconv"
	"strings"
)

// devIdentity holds the uid, gid, and home directory of the
// unprivileged user that runs inside the box.
type devIdentity struct {
	uid  int
	gid  int
	home string
}

// fallbackDevIdentity is the default when /etc/passwd has no dev
// entry.
var fallbackDevIdentity = devIdentity{
	uid:  fallbackDevID,
	gid:  fallbackDevID,
	home: "/home/dev",
}

// parsePasswdEntry looks up name in passwd-format content. It returns
// the identity and true if found, or false if the user is missing or
// malformed.
func parsePasswdEntry(content, name string) (devIdentity, bool) {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Split(line, ":")
		if len(fields) < 6 || fields[0] != name {
			continue
		}
		uid, uidErr := strconv.Atoi(fields[2])
		gid, gidErr := strconv.Atoi(fields[3])
		if uidErr != nil || gidErr != nil {
			return devIdentity{}, false
		}
		return devIdentity{uid: uid, gid: gid, home: fields[5]}, true
	}
	return devIdentity{}, false
}

// lookupDevIdentity reads the dev user from /etc/passwd. It falls
// back to uid/gid 501 in /home/dev if no usable entry exists.
func lookupDevIdentity() devIdentity {
	content, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return fallbackDevIdentity
	}
	if identity, ok := parsePasswdEntry(string(content), devUserName); ok {
		return identity
	}
	return fallbackDevIdentity
}

// childEnvironment returns environment with PRISON_TOKEN removed and
// HOME, USER, LOGNAME set for the dev user.
func childEnvironment(environment []string, dev devIdentity) []string {
	dropped := map[string]bool{
		"PRISON_TOKEN": true,
		"HOME":         true,
		"USER":         true,
		"LOGNAME":      true,
	}
	result := make([]string, 0, len(environment)+3)
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if !dropped[name] {
			result = append(result, entry)
		}
	}
	return append(result, "HOME="+dev.home, "USER="+devUserName,
		"LOGNAME="+devUserName)
}
