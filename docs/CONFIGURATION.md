# Configuration

Prison reads two configuration files and two allowlist files, and all are
optional. There are also a number of environment variables used for
configuration.

## Configuration files

`~/.prison/config.toml` is is a global configuration file that all Prison
projects follow.

The local per-project version is just a `prison.toml` file placed at the root
directory of the project.

Those files and all the keys are optional. The `.prison` directory is
automatically created on the first `prison up`.

The global config accepts:

```toml
[prison]
cage = "apple-container" # Which backend the boxes run on
inmates = ["claude"]     # Which tools should be included in every box

[checkpoint]
pager = "less -R"        # The program used for long diffs
diff_tool = "delta"      # Custom diff renderer
```

While the local config accepts:

```toml
[prison]
inmates = ["claude"] # The inmates that are installed in the Prison box

[box]
cpus = 4             # The number of CPUs to use
memory = "8G"        # The amount of memory to use
ports = [8888, 4321] # A list of ports to publish
sudo = false         # Whether or not root access inside the box will be allowed; you'll need this if you want to use `apt install` inside the box
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

A local `prison.toml` will not work until it's trusted with `prison trust`.
Any changes made to that file afterwards will need `prison trust` again. To
trust without a prompt, use the `--yes` flag.

#### `[prison]`

##### `cage` (global)

This determines which backend a box runs on. The default is `apple-container`
for macOS and `aws-firecracker` for Linux. To see all the available cages on
your build, run `prison cage list`.

This key is not available in the local `prison.toml`.

##### `inmates`

This determines the tools that are included with a box. By default, it is empty.
The local version outranks the global version in `~/.prison/config.toml`. Tools
named in this key in the local set is not unioned with the global set.

Use `[prison] inmates = []` to not use the Inmates defined by the global set.

#### `[box]`

##### `cpus`

This is a number from 1 to 32, with the default being 4.

#### `memory`

This is a number with no leading zero followed by `M` or `G`, with the default
being `8G`.

#### `sudo`

This determines whether or not `sudo` privilege is granted inside the box, with
the default being `false`.

This does not need to be `true` for things under `[setup] commands` to run with
`sudo`.

##### `ports`

This publises a guest port to `127.0.0.1` on the host machine. The default
publised ports are `3000`, `5173`, `8000`, and `8080`. Entries are between `1`
and `65535`. These guest ports are published on a block of four consecutive host
ports between `PRISON_PORT_BASE` and `PRISON_PORT_LIMIT`.

To have no ports published, use `[box] ports = []`.

##### `shadow`

This lists directories (project-relative) that is not mounted from the project
worktree to the box worktree. Use this to list build outputs and dependencies,
e.g., `node_modules`, `.venv`, etc.

Paths in here cannot be absolute or use `..`. The `.git` directory cannot be
included here since Prison already mounts over a part of it.

### `[setup]`

##### `commands`

These are commands that runs in order in the box after creation. Prison is able
to detect ecosystems and their corresponding lockfiles, and it will run the
right installers for each of them.

These commands only run once per box. Use `prison setup --force` to re-run them
all. They also re-run after a `prison rm`. For installs that failed partway
through, use `prison setup`.

#### `[checkpoint]`

##### `pager` (global)

Diffs that are longer than the terminal will be piped to the pager defined here.
It falls back to `$PAGER`, then `less -R` if `less` is installed on the host.

To disable paging entirely, set `[checkpoint] pager = ""`. Without a pager, the
diff is not generated in full with per-file and per-page caps. To keep the
outputs full, use `prison checkpoint diff --full`.

##### `diff_tool` (global)

This names a renderer, such as `delta`, `diff-so-fancy`, and `bat`, which Prison
will use to display diffs. (Make sure these tools are in your `$PATH`.)

Prison does not page the output when these tools are used since they often
include their own pager.

This also sets the diff renderer for `prison trust`.

##### `ignore`

This lists the patterns that are left out of checkpoints.

Without a slash, a pattern will match any entry at any depth. E.g., `*.log` will
match `.log` files anywhere in the worktree. A leading slash will anchor the
pattern to the project root directory. A trailing slash restricts a pattern to
directories. Patterns cannot be absolute or use `..`, and cannot use wildcards
crossing a slash.

The built-in list already covers `.git`.

Directories under `[box] shadow` is included in `[checkpoint] ignore` by
default.

#### `[egress]`

##### `hosts`

This lists the hostnames that the box can reach. This list is unioned with the
built-in allowlist or the global allowlist. The hostnames defined by Inmates
as their API upstreams are already whitelisted by default.

An example of the entries look like this:

| Pattern            | Covers                                    |
| ------------------ | ----------------------------------------- |
| `example.com`      | that host, on ports `80` and `443`        |
| `*.example.com`    | any subdomain and the bare domain itself  |
| `example.com:8443` | on `8443` instead of `80` and `443`       |
| `example.com:*`    | that host, on every port                  |

Entries cannot contain `/`, `@`, spaces, and tabs. There can also only be one
`:<port>` suffix. It also does not accept a wildcard anywhere other than as a
leading `*.` and a trailing `:*`. Ports cannot be outside the range of 1 to
65535.

#### `[environment]`

In here, the keys are the variable names. Names can consist of letters, digits,
and underscores. They can start with either a letter or an underscore. The
values can be texts, numbers, or booleans; and they can't carry a tab or a
newline.

For the names, `PRISON_*` cannot be used. The trust store variables
`SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS`, `REQUESTS_CA_BUNDLE`,
`CURL_CA_BUNDLE`, and `GIT_SSL_CAINFO` also cannot be used.

An Inmate can also declare its own `[environment]`, and when a name clashes with
the one there, this table wins.

#### `[secrets]`

##### `require`

This name the secrets that the project will need to work. The names are defined
when creating the secrets with `prison secret add <secret-name>`.

The secrets listed here still needs to be granted with
`prison secret grant <secret-name>`.

## Allowlist Files

`~/.prison/egress-allow` is a global allowlist file containing the hostnames
that a box can reach. When it's empty or absent, Prison falls back to the
built-in registries and source hosts. The presence of any rule this file
invalidates the built-in set.

The per-project version sits in `~/.prison/projects/<id>/egress-allow`. When the
global allowlist is empty or absent, the local config is unioned with the
built-in set. If a global allowlist is present, then the local config will be
unioned with it. The patterns in this file follows the rule of `[egress]`
[above](#egress).

## Environment variables

These outrank all the config files. They are useful for one-off runs, like:

```
PRISON_MEMORY=16G prison up
```

Below is a list of all the available environment variables that overrides the
config files:

| Variable            | Overrides                       | Default                            |
| ------------------- | ------------------------------- | ---------------------------------- |
| `PRISON_INMATES`    | `inmates` in both files         | nothing                            |
| `PRISON_CAGE`       | `cage` in the global file       | `apple-container`                  |
| `PRISON_PAGER`      | `checkpoint.pager`              | `$PAGER` and `less`, in that order |
| `PRISON_DIFF_TOOL`  | `checkpoint.diff_tool`          | Prison's own diff tool             |
| `PRISON_CPUS`       | `box.cpus`                      | `4`                                |
| `PRISON_MEMORY`     | `box.memory`                    | `8G`                               |
| `PRISON_SUDO`       | `box.sudo`, on only when `1`    | off (`0`)                          |
| `PRISON_PORTS`      | `box.ports`                     | `3000`, `5173`, `8000`, and `8080` |
| `PRISON_IMAGE`      | the resolved image              | resolved                           |
| `PRISON_FOUNDATION` | the image the base is built on  | per the manifest                   |

`PRISON_INMATES` and `PRISON_PORTS` are space-separated.

For `PRISON_FOUNDATION`, it overrides the `[image] foundation` value set by the
Inmates (if they're defined, that is; it's an optional key for them). This can
be something like `node:24-bookworm` and `debian:bookworm-slim`. This key exists
for runtimes that cannot be installed into the default image. When two Inmates
in the same project have a different `[image] foundation` values, the box will
refuse to build itself. Using this environment variable is a way to circumvent
that by forcefully equalizing the image used by both Inmates.

`PRISON_IMAGE` outranks everything and names the finished image. With this, the
`[image] foundation` and `PRISON_FOUNDATION` is not consulted. When you create
a box with this and that box lacks an Inmate's command, you will get a refusal.

The list of variables below have no config file equivalent. They are used to
tell Prison where to put things on the host machine:

| Variable             | Sets                              | Default     |
| -------------------- | --------------------------------- | ----------- |
| `PRISON_ROOT`        | the state root                    | `~/.prison` |
| `PRISON_DOMAIN`      | the suffix box hostnames get      | `prison`    |
| `PRISON_NETWORK`     | the network boxes attach to       | `prison`    |
| `PRISON_BROKER_PORT` | the port the broker binds         | `8787`      |
| `PRISON_PORT_BASE`   | first host port used to publish   | `40000`     |
| `PRISON_PORT_LIMIT`  | last host port used to publish    | `40100`     |

`PRISON_PORT_BASE` and `PRISON_PORT_LIMIT` defines the range of ports on the
host machine that is mapped to the guest ports defined in `PRISON_PORTS`. The
host ports are mapped in order according to availability.

## See which configuration is used

Use `prison status` to see what's configurations are resolved for the current
project:

```
project  /Users/you/projects/example
box      example-4d4e34b88286.prison
state    /Users/you/.prison/projects/4d4e34b88286
cage     apple-container, vm isolation
network  prison
sudo     no
inmates  claude
secrets  none
status   absent
```

## Healthcheck

Use `prison doctor` to print out a healthcheck of Prison in its current
configuration.
