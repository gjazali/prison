# Secrets

The secrets manager exist so that processes inside Prison that needs credential
access can do their work without ever seeing the credentials themselves.

The vault uses `XChaCha20-Poly1305` encryption with an `argon2id` derived key.
The version, derivation parameters, and salt are authenticated as additional
data.

## Initializing

To create the vault:

```sh
prison secret init
```

This will prompt you for a passphrase twice. Every command that reads or writes
the vault will ask for this same passphrase. To read the passphrase from a file
descriptor, use the `--passphrase-fd` flag. To unlock the vault for brokers
that's already running:

```sh
prison secret unlock
```

## Picking a mode

There are three modes available with the secrets manager: `route`, `sign`, and
`expose`. Only `expose` does not protect the secret itself; i.e., the secret
value itself is visible inside the box. The one you need depends on what the box
has to do with the credentials you put in as a secret.

### `route`

This is used for handling API calls that needs some credentials. This is
probably the most common case for the secrets manager. Below is how you would
use it:

```sh
printf '%s' "$STRIPE_SECRET_KEY" | prison secret add stripe \
  --mode route \
  --upstream api.stripe.com \
  --path-prefix /v1/ \
  --header Authorization \
  --header-prefix 'Bearer ' \
  --base-url-variable STRIPE_API_BASE
prison secret grant stripe
prison up
```

In the example above, the secret key itself is inputted through `stdin`. So you
can also omit the pipe expression there and be prompted for the credentials
you want to set as a secret. There is also an optional `--from-environment` flag
that you can use to pass an environment variable to have its value read.

This is how your code would look like:

```js
const response = await fetch(`${process.env.STRIPE_API_BASE}/v1/customers`, {
  method: "POST",
  headers: {
    "Authorization": `Bearer ${process.env.STRIPE_SECRET_KEY}`,
    "Content-Type": "application/x-www-form-urlencoded",
  },
  body: "...",
});

const customer = await response.json();
```

`STRIPE_API_BASE` would normally read as `https://api.stripe.com` in production
systems. But with prison, this environment variable would have the value of
`https://stripe.route.prison.internal` instead. It is set automatically by
Prison for the process, so no need to create a `.env` file manually for it.

Notice the `STRIPE_SECRET_KEY` in the example above. We didn't define that
anywhere during `prison secret add`. And most of the time, we won't have to,
since with most ecosystem, a nonexistent environment variable is read as empty.
Besides, all the credential and forwarded headers listed below are stripped from
the original request.

Credential headers:

- `Authorization`
- `X-Api-Key`
- `Api-Key`
- `Proxy-Authorization`

Forwarded headers:

- `Forwarded`
- `X-Forwarded-For`
- `X-Forwarded-Host`
- `X-Forwarded-Proto`
- `X-Real-Ip`

This means that whatever they're filled with in the original request is
irrelevant. These headers are re-added with the actual secrets later by the
broker if they're specified by the `--header` flag during `prison secret add`.

If you're working with an SDK and it checks the existence of environment
variables and/or validates their values—and crashes on failure—you can specify
the `--token-variable` and `--token-placeholder` flags (the latter is required
if you define the former) like:

```sh
printf '%s' "$STRIPE_SECRET_KEY" | prison secret add stripe \
  --mode route \
  --upstream api.stripe.com \
  --path-prefix /v1/ \
  --header Authorization \
  --header-prefix 'Bearer ' \
  --base-url-variable STRIPE_API_BASE \
  --token-variable STRIPE_SECRET_KEY \
  --token-placeholder 'sk_test_xxxxx'
```

Make sure to consult the documentations of whatever API you're trying to call to
figure out the correct token formatting.

Sometimes a codebase might hardcode the *actual hostnames* themselves, and you
want to avoid changing them into environment variable calls like with the
examples above. To deal with that, use the `--intercept` flag.

```sh
printf '%s' "$STRIPE_SECRET_KEY" | prison secret add stripe \
  --mode route \
  --upstream api.stripe.com \
  --path-prefix /v1/ \
  --header Authorization \
  --header-prefix 'Bearer ' \
  --intercept
```

The client would call something like `https://api.stripe.com/v1/charges` and
Prison answers the TLS connection, mints a certificate for that name, terminates
the TLS, injects the credential, and forwards the request.

Each intercepting route gets its own certificate authority with a critical
`nameConstraints` extension. That authority can only sign certificates for the
one host it's set to.

The connection termination from prison is `HTTP/1.1`-only. If the client inside
a box requires `HTTP/2` or does certificate-pinning (e.g., Java `TrustManager`),
then `--intercept` will not work.

To revoke a secret:

```sh
prison secret revoke stripe
```

On the next request, the Prison box will no longer be able to use the
credential.

To see a list of all the secrets defined in Prison:

```sh
prison secret list
```

And to see all the current project's grants:

```sh
prison secret grants
```

### `sign`

The `sign` mode allows you to use credentials that you don't want to store in
the Prison box to sign things inside the Prison box.

For example, if you want to be able to sign your Git commits with your SSH key,
which is located in `~/.ssh/id_ed25519.pub` on your machine:

```sh
prison secret add commit-key \
  --mode sign \
  --algorithm ssh-agent \
  --fingerprint "$(ssh-keygen -lf ~/.ssh/id_ed25519.pub | awk '{print $2}')"
prison secret grant commit-key
prison up
```

Notice that you don't have to give it your actual key. This is because the key
stays with the SSH agent; Prison only stores the fingerprint. When a signature
is requested, Prison will ask the `ssh-agent` running on your machine.

The fingerprint must start with `SHA256:`. Run `ssh-add -l` to see yours.

Inside the box, `/home/dev/.prison/bin/prison-ssh-sign` stands in for
`ssh-keygen -Y sign`:

```sh
git config gpg.format ssh
git config gpg.ssh.program /home/dev/.prison/bin/prison-ssh-sign
git config user.signingkey "$(/home/dev/.prison/bin/prison-ssh-sign --print-public-key)"
git config commit.gpgsign true
```

### `expose`

This is the same as declaring an environment variable and putting the actual
credential value inside a Prison box.

```sh
printf '%s' "$DATABASE_URL" | prison secret add db-url \
  --mode expose \
  --variable DATABASE_URL
prison secret grant db-url
prison up
```

Adding this through `prison secrets` doesn't give you any extra security. This
is mainly for easier organization of credentials.

## Configuration

In [`CONFIGURATION.md`](https://github.com/gjazali/prison/blob/main/docs/CONFIGURATION.md),
you might notice the following kind of table for `~/prison.toml`:

```toml
[secrets]
require = ["stripe", "commit-key"]
```

This only defines the secrets that the project will need. Granting the secrets
still has to be done manually through `prison secrets grant`. This is also
another advantage of using `--mode expose` rather than manually declaring the
environment variables in the box yourself: it'll be a lot easier to notice that
they're missing.

## Sharing secrets across projects

The vault holds only one copy of every defined secrets. Which means, to share a
secret that was defined for one project, simply grant it to the other project.
The command `prison secret show <secret-name>` lists the project IDs holding
the grant.

## Viewing the logs

The logs records every usage of a secret:

```sh
prison secret log                    # For this project
prison secret log --all              # For every project
prison secret log --secret stripe    # For one secret
prison secret log stripe             # An alias of `--secret stripe`
```

You can use the `--limit` flag to define how many of the newest log lines to
print.

To see the broker traffic:

```sh
prison broker log --kind <secret-mode>
```

## Rotating a secret

Simply add the credential again under the same name to replace it. Note that all
the flags used in the original `prison secret add` (if you still need them) must
be used again here.

## Removing a secret

Simply do the following to delete a secret:

```sh
prison secret rm <secret-name>
```

## List of flags for all secret modes

### All modes

#### `--mode`

**Default:** Required

**Values:** The mode for the secret (`route`, `sign`, `expose`).

#### `--description`

**Default:** Empty string

**Value:** Any kind of description of what the secret is used for

#### `--confirm`

**Default:** `false`

**Value:** Whether or not Prison will prompt the host every time the secret is
used (`true`, `false`).

#### `--from-environment`

**Default:** Empty string

**Value:** Name of the host machine environment variable with the secret value

This makes it so that the secret value is read from the host environment
variable instead of `stdin`.

### Route-only

#### `--upstream`

**Default:** Required (when not specified, the port of this hostname is always
443, and TLS is always used)

**Value:** The hostname of the upstream the secret is used for, and optionally,
the port.

Prison will route the original request, which goes to an internal address, to
this hostname.

#### `--path-prefix`

**Default:** `/`

**Value:** Any path prefix that comes after the hostname specified by the
`--upstream` flag.

Only requests whose path starts with this will be forwarded to the proper
hostname.

#### `--header`

**Default:** `Authorization`

**Value:** The name of the HTTP header the credential is to be injected into

#### `--header-prefix`

**Default:** Empty string

**Value:** The prefix to be appended in the `--header` header value

A common example of this is `Bearer`, which means the header content would be:

```
Authorization: Bearer sk_test_xxxxx
```

#### `--base-url-variable`

**Default:** Empty string

**Value:** Environment variable name set in the box that points to the upstream
hostname.

Internally, it points to an address like `http://<name>.route.prison.internal`.
Traffic that goes to this address is forwarded by the broker depending on the
validity.

#### `--token-variable`

**Default:** Empty string

**Value:** Name of environment variable whose value stands in as the secret.

This is mainly used to prevent crashes when using SDKs that validates the
existance of the credential variable. It is filled by the dummy credential
defined in `--token-placeholder`.

#### `--token-placeholder`

**Default:** Empty string (required when `--token-variable` is defined)

**Value:** The dummy credential value that fills the environment variable
defined in `--token-variable`.

This is mainly used to prevent crashes when using SDKs that validates the
structure of the credential value.

#### `--methods`

**Default:** Any HTTP method

**Value:** Comma-separated HTTP methods that are allowed for requests.

Valid HTTP request methods, according to
[MDN](https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Methods):

- `GET`
- `HEAD`
- `POST`
- `PUT`
- `DELETE`
- `CONNECT`
- `OPTIONS`
- `TRACE`
- `PATCH`

#### `--rate-limit`

**Default:** `0` (unlimited)

**Value:** The maximum number of requests allowed per minute.

#### `--intercept`

**Default:** `false`

**Value:** Whether the broker will terminate TLS for SDKs that hardcode the
upstream hostname (`true`, `false`).

### Sign-only

#### `--algorithm`

**Default:** `ssh-agent`

**Value:** The signing algorithm (`ssh-agent`, `hmac-sha256`).

#### `--fingerprint`

**Default:** Empty string (required when `--algorithm` is `ssh-agent`)

**Value:** Fingerprint of the key in your SSH agent.

#### `--namespace`

**Default:** `git`

**Value:** The SSHSIG namespace

Git expects the value of this to be `git`.

### Expose-only

#### `--variable`

**Default:** Empty string

**Value:** The environment variable name used to contain the credential value
inside the box.
