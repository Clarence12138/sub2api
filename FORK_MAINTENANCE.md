# Fork Maintenance

This fork keeps `main` as the deployable branch:

- `origin` => `git@github.com:Clarence12138/sub2api.git`
- `upstream` => `https://github.com/Wei-Shaw/sub2api.git`

## Sync upstream changes

Use the helper script:

```bash
./scripts/sync-upstream.sh
```

It will:

1. fetch `upstream` and tags
2. switch to `main`
3. rebase `main` onto `upstream/main`

If upstream touched the same area as the local customizations, resolve the conflict, then continue:

```bash
git rebase --continue
```

After the rebase:

```bash
git push origin main --force-with-lease
```

## Release your own image

This fork publishes to:

```text
ghcr.io/clarence12138/sub2api
```

The release workflow runs when a tag like this is pushed:

```bash
git tag v0.1.116-clarence.1
git push origin main --tags
```

That tag will publish:

```text
ghcr.io/clarence12138/sub2api:0.1.116-clarence.1
ghcr.io/clarence12138/sub2api:latest
```

## Current customization

This fork currently carries one product-specific behavior change:

- Align ops dashboard TTFT diagnosis with the configured `ttft_p99_ms_max` threshold instead of a hard-coded `500ms`.
