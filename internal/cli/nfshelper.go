package cli

import (
	"log"
	"os"

	"github.com/spf13/cobra"

	"prison/internal/cage/firecracker"
	"prison/internal/nfsexport"
)

// newHostNFSHelperCommand runs as root for the aws-firecracker cage.
// Systemd starts one helper per user from a socket.
func newHostNFSHelperCommand() *cobra.Command {
	var uid, gid int
	command := &cobra.Command{
		Use:    "nfs-helper",
		Short:  "export folders to boxes for one user",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			listener, err := nfsexport.ActivatedListener()
			if err != nil {
				return err
			}
			policy := nfsexport.Policy{UID: uid, GID: gid,
				AllowClient: firecracker.IsBoxAddress}
			return nfsexport.Serve(listener, policy,
				log.New(os.Stderr, "", 0))
		},
	}
	command.Flags().IntVar(&uid, "uid", -1, "the user that can export")
	command.Flags().IntVar(&gid, "gid", -1, "the group of the exports")
	command.MarkFlagRequired("uid")
	command.MarkFlagRequired("gid")
	return command
}
