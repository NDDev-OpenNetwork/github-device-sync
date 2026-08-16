# Estate

An estate is the declarative answer to "which repositories exist, who owns
them, and what should be true of each". It holds intent only. Provider
observations, repository lists, installation tokens, private keys, local paths,
branch state and operation journals are all deliberately absent, and nothing
here may become a place where they appear.

## What is in this directory

The estate shipped here is an **example**. It is complete and schema-valid, so
the test suite and `gds validate estate` run against it unchanged, but every
login in it belongs to GitHub's reserved `example-*` space and describes no real
account.

| file | what it declares |
| --- | --- |
| `estate.yaml` | the estate itself: which installations exist, policy precedence, rollout limits |
| `owners/` | one file per account, mapping a provider login to an owner identity |
| `installations/` | how repositories are discovered, and with what read scope |
| `mutations/` | the separate, narrower credential that may write, and exactly which operations |
| `selectors/` | rules that classify discovered repositories into portfolios and policy profiles |
| `devices/` | where a given machine materializes repositories, and which harnesses it runs |
| `exceptions/` | sparse overrides for repositories a selector classifies wrongly |

## Using your own estate

Keep it outside this repository and point the CLI at it:

```
gds --cwd ~/my-estate validate estate
gds --cwd ~/my-estate reconcile --plan
```

That separation is not organisational tidiness. An estate names your accounts,
your repositories and your machines; a public engine repository is the wrong
place for it, and vendoring it here would mean a merge conflict every time you
took an update.

## Why discovery and mutation are two installations

Reading the estate needs no write scope anywhere. Giving the discovery
credential write access so that one command can also apply changes would make
every read operation capable of a write it never performs. So the credential
that can write is a separate installation, holds `repository_selection:
selected` rather than `all`, and enumerates the operations it may perform. The
`gates` block then refuses the irreversible ones outright.
