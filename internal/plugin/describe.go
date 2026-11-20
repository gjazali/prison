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
	if inmate.Image.VersionArg != "" {
		field(w, "version arg", inmate.Image.VersionArg)
	}
	if len(inmate.Image.MovingTags) > 0 {
		field(w, "moving tags", strings.Join(inmate.Image.MovingTags, ", "))
	}
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
	if inmate.Auth.Upstream != "" {
		field(w, "upstream", inmate.Auth.Upstream+inmate.Auth.PathPrefix)
	}
	if hosts := describeInterceptHosts(inmate.Auth); len(hosts) > 0 {
		field(w, "intercept", strings.Join(hosts, ", "))
	}
	if inmate.Auth.PlaceholderTokenURL != "" {
		field(w, "oauth stub", inmate.Auth.PlaceholderTokenURL)
	}
	if inmate.Auth.BaseURLVariable != "" {
		field(w, "base url", inmate.Auth.BaseURLVariable)
	}
	if inmate.Auth.TokenVariable != "" {
		field(w, "token", inmate.Auth.TokenVariable)
	}
	for _, credential := range inmate.Auth.Credentials {
		detail := fmt.Sprintf("%s in %s%s",
			credential.Variable, credential.Header,
			describePrefix(credential.Prefix))
		if credential.TokenURL != "" {
			detail += fmt.Sprintf(" (oauth via %s)", credential.TokenURL)
		}
		field(w, "credential", detail)
	}
	if inmate.Auth.InBox != nil && len(inmate.Auth.InBox.Hosts) > 0 {
		field(w, "in-box", strings.Join(inmate.Auth.InBox.Hosts, ", "))
	}
}

func describeInterceptHosts(auth *AuthTable) []string {
	var hosts []string
	if auth.Intercept && auth.Upstream != "" {
		hosts = append(hosts, auth.Upstream)
	}
	hosts = append(hosts, auth.InterceptHosts...)
	return hosts
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
