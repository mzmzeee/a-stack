# contrib

- `systemd/a-stack.service`: the systemd user unit. See [Start a-stack automatically](../docs/install.md#start-a-stack-automatically).
- `pi/a-stack.ts`: the Pi extension that `a-stack hook install --pi` writes. See [Pi](../docs/hooks.md#pi).
- `opencode/a-stack.js`: the OpenCode plugin that `a-stack hook install --opencode` writes. See [OpenCode](../docs/hooks.md#opencode).
- `aur/PKGBUILD`: the AUR package `a-stack-bin`, from the release archives.
- `homebrew/a-stack.rb`: a Homebrew formula for Linux and macOS, from the release archives.
- `scoop/a-stack.json`: a Scoop manifest for Windows, from the release archives.

The packages install the release binary of one version and check it against that release's `SHA256SUMS`. The Homebrew formula is published in [mzmzeee/homebrew-tap](https://github.com/mzmzeee/homebrew-tap) and the Scoop manifest in [mzmzeee/scoop-bucket](https://github.com/mzmzeee/scoop-bucket). Each of those repositories has a workflow that installs the package on GitHub's macOS, Linux or Windows machines on every push. The AUR package is not published yet. Nothing in this repository publishes automatically; the steps are below.

## Update a package to a new release

After the release workflow has published `vX.Y.Z`:

1. Download its checksums: `curl -fsSL -O https://github.com/mzmzeee/a-stack/releases/download/vX.Y.Z/SHA256SUMS`. Check the signature first, as in [How releases are verified](../docs/install.md#how-releases-are-verified).
2. In `aur/PKGBUILD`, set `pkgver=X.Y.Z` and `pkgrel=1`. Copy the sums of `a-stack.service`, `a-stack_linux_amd64.tar.gz` and `a-stack_linux_arm64.tar.gz` from `SHA256SUMS`. The LICENSE sum changes only when the license file changes (`sha256sum LICENSE`). Or run `updpkgsums` from `pacman-contrib`, which downloads the files and writes all sums.
3. In `homebrew/a-stack.rb`, set the four `url`s to the new version, and the four archive sums. Homebrew reads the version from the URLs; `brew audit --strict` refuses a `version` line that repeats it.
4. In `scoop/a-stack.json`, set `version` and both `url`s to the new version, and the sums of `a-stack_windows_amd64.zip` and `a-stack_windows_arm64.zip`. Or, on Windows with Scoop, run `& "$(scoop prefix scoop)\bin\checkver.ps1" -Dir contrib\scoop -App a-stack -Update`, which finds the latest release and takes the sums from its `SHA256SUMS`.
5. Commit the files here.

## Publish to the AUR

Once, create an account on [aur.archlinux.org](https://aur.archlinux.org) and add your SSH key. Then, for each version:

```sh
git clone ssh://aur@aur.archlinux.org/a-stack-bin.git
cd a-stack-bin
cp /path/to/a-stack/contrib/aur/PKGBUILD .
makepkg -f                      # downloads, checks the sums, builds the package
namcap PKGBUILD ./*.pkg.tar.zst
makepkg --printsrcinfo > .SRCINFO
git add PKGBUILD .SRCINFO
git commit -m "Update to X.Y.Z"
git push
```

The first push creates the package. namcap warns that the binary lacks PIE and full RELRO, because the release binary is static; that is expected. Users then install it with an AUR helper, such as `yay -S a-stack-bin`. The package puts the binary in `/usr/bin/a-stack` and the systemd user unit in `/usr/lib/systemd/user/a-stack.service`, with `ExecStart` changed to `/usr/bin/a-stack`, so users start it with `systemctl --user enable --now a-stack`. `a-stack update` cannot replace `/usr/bin/a-stack`; it stops and names the folder. Users update with their package manager.

## Publish the Homebrew tap

For each version:

```sh
git clone https://github.com/mzmzeee/homebrew-tap.git
mkdir -p homebrew-tap/Formula
cp /path/to/a-stack/contrib/homebrew/a-stack.rb homebrew-tap/Formula/
cd homebrew-tap
brew audit --strict --formula Formula/a-stack.rb
brew install --formula ./Formula/a-stack.rb && brew test a-stack
git add Formula/a-stack.rb
git commit -m "a-stack X.Y.Z"
git push
```

Users then run `brew install mzmzeee/tap/a-stack`. The formula covers Linux and macOS (13 or later) on amd64 and arm64. It installs only the binary; for the service, users run `a-stack service install` (see [Start a-stack automatically](../docs/install.md#start-a-stack-automatically)). The service then runs the binary by its real path in the Homebrew Cellar, which changes with each version, so users run `a-stack service install` again after `brew upgrade`. Users update with `brew upgrade`, not `a-stack update`.

## Publish the Scoop bucket

For each version, on Windows:

```powershell
git clone https://github.com/mzmzeee/scoop-bucket.git
New-Item -ItemType Directory -Force scoop-bucket\bucket
Copy-Item \path\to\a-stack\contrib\scoop\a-stack.json scoop-bucket\bucket\
scoop install .\scoop-bucket\bucket\a-stack.json   # downloads the archive and checks its sum
a-stack version
scoop uninstall a-stack
cd scoop-bucket
git add bucket/a-stack.json
git commit -m "a-stack: Update to version X.Y.Z"
git push
```

Users then run `scoop bucket add mzmzeee https://github.com/mzmzeee/scoop-bucket` and `scoop install mzmzeee/a-stack`. Scoop puts the binary in `~\scoop\apps\a-stack\` and a shim on the PATH. For the service, users run `a-stack service install`. Users update with `scoop update a-stack`; `a-stack update` refuses to replace a binary in a Scoop folder and names that command. Scoop cannot replace a running binary, so users stop the service first (disable and end the task named by `a-stack service status`) and enable/run it again after the update. See the [Windows service commands](../docs/install.md#on-windows-with-task-scheduler).
