package plugin

import (
	"fmt"
	"io"
	"strings"
)

func Describe(inmate *Inmate, w io.Writer) {
	field(w, "name", inmate.Name)
	for _, alias := range inmate.Inmate.Aliases {
		field(w, "alias", alias)
	}
	if inmate.Problem != nil {
		field(w, "origin", describeOrigin(inmate))
		field(w, "problem", inmate.Problem.Error())
		return
	}
	field(w, "summary", inmate.Inmate.Description)
	field(w, "origin", describeOrigin(inmate))
	field(w, "approved", yesOrNo(inmate.Trusted))
	if inmate.Image.Foundation != "" {
		field(w, "foundation", inmate.Image.Foundation)
	}
	field(w, "dockerfile", inmate.Image.Dockerfile)
	field(w, "command", inmate.Command.Run)
	if inmate.Command.Unsafe != "" {
		field(w, "unsafe", inmate.Command.Unsafe)
	}
	describeAuth(inmate, w)
	for _, guestPath := range inmate.Persist.Paths {
		field(w, "persist", guestPath)
	}
	for _, assignment := range inmate.EnvironmentAssignments() {
		field(w, "environment", assignment)
	}
	for _, host := range inmate.Egress.Hosts {
		field(w, "egress", host)
	}
	for _, line := range hostSummary(&inmate.Manifest) {
		field(w, "host", line)
	}
	if inmate.Hooks.BoxCommand != "" {
		field(w, "box", inmate.Hooks.BoxCommand)
	}
}

func describeAuth(inmate *Inmate, w io.Writer) {
	if inmate.Auth == nil {
		return
	}
	field(w, "upstream", inmate.Auth.Upstream+inmate.Auth.PathPrefix)
	field(w, "base url", inmate.Auth.BaseURLVariable)
	field(w, "token", inmate.Auth.TokenVariable)
	for _, credential := range inmate.Auth.Credentials {
		field(w, "credential", fmt.Sprintf("%s in %s%s",
			credential.Variable, credential.Header,
			describePrefix(credential.Prefix)))
	}
}

func describePrefix(prefix string) string {
	if prefix == "" {
		return ""
	}
	return fmt.Sprintf(" behind %q", prefix)
}

func describeOrigin(inmate *Inmate) string {
	if inmate.Bundled {
		return "bundled with prison"
	}
	origin := &strings.Builder{}
	fmt.Fprintf(origin, "installed at %s", inmate.Dir)
	url, ref := inmate.Origin()
	if url != "" {
		fmt.Fprintf(origin, ", from %s", url)
	}
	if ref != "" {
		fmt.Fprintf(origin, " at %s", ref)
	}
	return origin.String()
}

func yesOrNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
