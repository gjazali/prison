# Prison

***Note:*** *Right now, Prison only supports macOS. It will support Linux in the
future with an AWS Firecracker Cage. Documentations on the architecture,
usage of the secrets manager, Inmate and Cage writing, etc., are in progress and
will also be released soon.*

Run processes in a per-project micro-VM, in isolation from your machine.

There are many use cases for Prison. The most notable one today is probably for
running AI agents without them having access to your entire computer's file.

Prison also protects your credentials, like your signing keys and API keys from
the processes you chose to isolate inside it. This is done through a dedicated
secrets manager.

Prison uses a proxy system to ensure that credentials cannot be seen by anything
inside a Prison box, even if these credentials are needed by processes running
in isolation inside that box.
See [`docs/ARCHITECTURE.md`](https://github.com/gjazali/prison/blob/main/docs/ARCHITECTURE.md)
for more information on the architecture that enable the security of Prison.

When first-class integration is written for a specific tool to run in Prison,
that tool can be referred to as an **Inmate** (Claude Code is an example of an
inmate that is shipped with Prison). A micro-VM back-end that is integrated into
Prison, meanwhile, is called a **Cage** (Prison supports Apple's
[Container](https://opensource.apple.com/projects/container/) as a Cage
on macOS and is planning to support Amazon's
[Firecracker](https://firecracker-microvm.github.io/) on Linux).

Both Inmates and Cages use a plugin system, so anyone can write their own and
users can install them easily.

## Quick Start

### Installing Prison and Its Dependencies

Prison can be installed by cloning this repository and then building it:

```
git clone https://github.com/gjazali/prison.git ~/prison
cd prison
make
sudo ln -s ~/prison/bin/prison /usr/local/bin/prison
```

On macOS, Prison requires Apple's
[Container](https://opensource.apple.com/projects/container/) to be installed.
You can install it through:

```
brew install container
container system start --enable-kernel-install
export PATH="~/prison/bin:$PATH"
```

Restart your terminal after that.

### Configuring a Project for Prison

A per-project configuration file by the name of `prison.toml` can be defined
to control Prison's behavior for that project specifically:

```toml
[prison]
inmates = ["claude"] # The inmates that are installed in the Prison box

[box]
sudo = false         # Whether or not root access inside the box will be allowed; you'll need this if you want to use `apt install` inside the box
ports = [8888, 4321] # A list of ports to publish
shadow = [".venv"]   # Files and folder that will be kept out of the Prison box's worktree

[setup]
# These commands will run in the box right after it's created
commands = [
  "sudo apt-get update",
  "sudo apt-get -y install python3.11",
  "sudo apt-get -y install python3.11-venv",
  "python3.11 -m venv .venv",
]

[checkpoint]
# These files will be ignored by Prison's checkpoints
ignore = [
  "*.log",
  "data/"
]

[egress]
# Processes inside Prison are allowed to access these hostnames
hosts = [
  "api.example.com",  # on port 80 and 443
  "example.org:8081", # on port 8081
  "example.net:*",    # on every port
]

[secrets]
# These define the credentials in the secrets manager that is needed by the box
require = [
  "umls-api-key" # The name is defined through `prison secrets`
]

[environment]
PYTHON_VERSION = "3.11" # The environment variables the box will start with
```

Prison will run with its default behavior without this file. Every time this
file is updated, you will have to approve the edits by running:

```
prison trust
```

It's designed this way because the `prison.toml` is also mounted to the isolated
box, which means processes inside the box can change it. Prison will ignore
*everything* in `prison.toml` unless it's been trusted, even if the change is
just one value in one table.

Also note that every time you add to the `[prison] inmates` table, you'll need
to re-run `prison up` to update the list of inmates actually present in the box.
This is because the box image is determined by this set of enabled inmates.

### Using Prison

To use Prison, enter the project directory you want Prison to isolate. And then,
run

```
prison up
```

to start the box. You can spawn a shell of the Prison box by using the command:

```
prison shell
```

To use an inmate inside the box, directly from the host
machine's terminal, run:

<pre>
prison <b><i>inmate_name</i></b>
</pre>

Find the valid Inmate names through:

```
prison inmate list
```

For each of these Inmates, you can get more details about them through:

<pre>
prison inmate show <b><i>inmate_name</i></b>
</pre>

If the Inmate requires some authentication, be sure to set them under one of the
environment variables listed in the output of that command, labeled by
`auth-credential`. (Be sure to set them in the *host machine*, not the Prison
box.)

Here's an example of what you can do with Claude Code, which is one of the
available Inmates shipped with Prison:

<pre>
export CLAUDE_CODE_OAUTH_TOKEN=<b><i>oauth_token</i></b>
prison up
prison claude
</pre>

`CLAUDE_CODE_OAUTH_TOKEN` (or `ANTHROPIC_API_KEY`) is defined by
`prison inmate show claude` as the environment variables for the credentials.

The `unsafe-command` variant of `claude` (claude --dangerously-skip-permissions)
can be triggered through `prison claude --yolo`.

### Managing Prison Boxes

You can list all the Prison boxes that are running through

```
prison list
```

and you can remove them by running

<pre>
prison rm <b><i>name</i></b>
</pre>

where `name` can be the full name listed by the `BOX` column from `prison list`
or simply its hash. This will destroy the particular box named in `name` but
its state directory will be kept underneath `~/.prison/`. To list the state
directories of Prison, run

```
prison list --state
```

To delete a state directory (alongside the Prison box, if hasn't been `rm`'d
independently yet), do

<pre>
prison rm --state <b><i>name</i></b>
</pre>

If you wish to only stop a box instead of destroying it, use the `stop`
command:

<pre>
prison stop <b><i>name</i></b>
</pre>

### Taking Checkpoints

Once in a while, you may have the need to save a checkpoint of your porject's
state for experiments without messing with source control (e.g., Git).

To save a checkpoint on the current state of the worktree, simply do:

```
prison checkpoint take
```

The command `prison checkpoint` alone defaults to the `take` subcommand. You
can also include a label with the checkpoint using:

<pre>
prison checkpoint take --label <b><i>label</i></b>
</pre>

Listing all the checkpoints you've made is straightforward:

```
prison checkpoint list
```

To see what's changed in the worktree between the current state of the worktree
and a particular checkpoint, do:

<pre>
prison checkpoint diff <b><i>id</i></b>
</pre>

Drop the `id` part to compare the current worktree to the last checkpoint.
To go back to a checkpoint, use the `restore` subcommand.

<pre>
prison checkpoint restore <b><i>id</i></b>
</pre>

To go back and forth by steps, do:

```
prison checkpoint back
prison checkpoint forward
```

And to forget a checkpoint, simply run:

<pre>
prison checkpoint remove <b><i>id</i></b>
</pre>

The `.git` directory is ignored by the checkpointing system by default.

### Using the Secrets Manager

The secrets manager exist so that processes inside Prison that needs credential
access can do their work without ever seeing the credentials themselves.

Most people would use the `route` mode with the `--intercept` flag. This way,
your project code doesn't have to change and no proxy-awareness is needed by the
client (proxy-awareness is like when Node.js needs `NODE_USE_ENV_PROXY=1` to be
set for proxies to work properly).

To use that mode specifically with your secrets, you can do something like

```
printf '%s' "$STRIPE_SECRET_KEY" | prison secret add stripe \
  --mode route \
  --upstream api.stripe.com \
  --path-prefix /v1/ \
  --header Authorization \
  --header-prefix 'Bearer ' \
  --intercept
```

which will pipe your Stripe key stored in `STRIPE_SECRET_KEY` into the secrets
manager. Now, anyime processes in the Prison box calls
`https://api.stripe.com/v1/*`, Prison answers the `CONNECT` itself with a
certificate it mints for that name, terminates the TLS, injects the header, and
forwards.

To actually have that work, you have to grant the box the ability to use that
secret first:

```
prison secret grant stripe

```

For other ways of using the secrets manager, please refer to
[`docs/SECRETS.md`](https://github.com/gjazali/prison/blob/main/docs/SECRETS.md).
