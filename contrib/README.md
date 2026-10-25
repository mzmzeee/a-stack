# contrib

- `systemd/a-stack.service`: the systemd user unit. See [Start a-stack automatically](../docs/install.md#start-a-stack-automatically).
- `aur/PKGBUILD`: the AUR package `a-stack-bin`, from the release archives.
- `homebrew/a-stack.rb`: a Homebrew formula for Linux, from the release archives.
- `scoop/a-stack.json`: a Scoop manifest for Windows, from the release archives.

The packages install the release binary of one version and check it against that release's `SHA256SUMS`. They are not published yet. The steps below publish them; nothing in this repository does it automatically.

## Update a package to a new release

After the release workflow has published `vX.Y.Z`:

1. Download its checksums: `curl -fsSL -O https://github.com/mzmzeee/a-stack/releases/download/vX.Y.Z/SHA256SUMS`. Check the signature first, as in [How releases are verified](../docs/install.md#how-releases-are-verified).
2. In `aur/PKGBUILD`, set `pkgver=X.Y.Z` and `pkgrel=1`. Copy the sums of `a-stack.service`, `a-stack_linux_amd64.tar.gz` and `a-stack_linux_arm64.tar.gz` from `SHA256SUMS`. The LICENSE sum changes only when the license file changes (`sha256sum LICENSE`). Or run `updpkgsums` from `pacman-contrib`, which downloads the files and writes all sums.
3. In `homebrew/a-stack.rb`, set `version` and both `url`s to the new version, and the two archive sums.
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

Once, create a public GitHub repository `mzmzeee/homebrew-tap`. Then, for each version:

```sh
git clone git@github.com:mzmzeee/homebrew-tap.git
mkdir -p homebrew-tap/Formula
cp /path/to/a-stack/contrib/homebrew/a-stack.rb homebrew-tap/Formula/
cd homebrew-tap
brew audit --strict --formula Formula/a-stack.rb
brew install --formula ./Formula/a-stack.rb && brew test a-stack
git add Formula/a-stack.rb
git commit -m "a-stack X.Y.Z"
git push
```

Users then run `brew install mzmzeee/tap/a-stack`. The formula is for Linux only, because v0.1.4 has no macOS archives. For the first release with macOS archives, add an `on_macos` block with `a-stack_darwin_amd64.tar.gz` and `a-stack_darwin_arm64.tar.gz` and their sums, and remove `depends_on :linux`. It installs only the binary; for the service, users run `a-stack service install` (see [Start a-stack automatically](../docs/install.md#start-a-stack-automatically)). The service then runs the binary by its real path in the Homebrew Cellar, which changes with each version, so users run `a-stack service install` again after `brew upgrade`. Users update with `brew upgrade`, not `a-stack update`.

## Publish the Scoop bucket

No release has Windows archives yet, so the manifest has the version 0.1.6 and placeholder sums of zeros. Before the first publish, set the version and sums of the first release with Windows archives, as in step 4 above. `scoop install` refuses the placeholder sums.

Once, create a public GitHub repository `mzmzeee/scoop-bucket`. Then, for each version, on Windows:

```powershell
git clone git@github.com:mzmzeee/scoop-bucket.git
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

Users then run `scoop bucket add mzmzeee https://github.com/mzmzeee/scoop-bucket` and `scoop install mzmzeee/a-stack`. Scoop puts the binary in `~\scoop\apps\a-stack\` and a shim on the PATH. For the service, users run `a-stack service install`. Users update with `scoop update a-stack`; `a-stack update` refuses to replace a binary in a Scoop folder and names that command. Scoop cannot replace a running binary, so users stop the service first (`schtasks /End /TN a-stack`) and start it again after the update (`schtasks /Run /TN a-stack`). This has not been tried on Windows yet.
