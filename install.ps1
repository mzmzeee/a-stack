# Install the a-stack binary from GitHub Releases on Windows.
#
#   irm https://github.com/mzmzeee/a-stack/releases/latest/download/install.ps1 | iex
#
# It runs in Windows PowerShell 5.1 and PowerShell 7, on Windows 10 or later,
# x64 or Arm. It checks the SHA-256 checksum of the archive against SHA256SUMS
# before it installs anything. It does not check the release signature:
# PowerShell cannot check an Ed25519 signature, and this script comes from the
# same release as the archive. a-stack update checks the signature of every
# later update.
#
# Environment:
#   A_STACK_VERSION      release tag to install, such as v0.1.6 (default: latest)
#   A_STACK_INSTALL_DIR  where to put a-stack.exe
#                        (default: %LOCALAPPDATA%\Programs\a-stack)
#   A_STACK_NO_PATH      set to 1 to leave your user PATH as it is
#   A_STACK_BASE_URL     release download URL, for mirrors and tests
#                        (default: https://github.com/mzmzeee/a-stack/releases)

function Install-AStack {
	$ErrorActionPreference = 'Stop'
	$ProgressPreference = 'SilentlyContinue' # the progress bar makes downloads slow in PowerShell 5.1
	$repo = 'https://github.com/mzmzeee/a-stack/releases'
	if ($env:A_STACK_BASE_URL) { $repo = $env:A_STACK_BASE_URL.TrimEnd('/') }
	$version = 'latest'
	if ($env:A_STACK_VERSION) { $version = $env:A_STACK_VERSION }
	$dir = Join-Path $env:LOCALAPPDATA 'Programs\a-stack'
	if ($env:A_STACK_INSTALL_DIR) { $dir = $env:A_STACK_INSTALL_DIR }

	# OSArchitecture names the machine, also in an x64 PowerShell on Arm.
	$machine = "$env:PROCESSOR_ARCHITECTURE"
	try { $machine = [string][System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture } catch { }
	switch ($machine) {
		{ $_ -in 'X64', 'AMD64' } { $arch = 'amd64' }
		{ $_ -in 'Arm64', 'ARM64' } { $arch = 'arm64' }
		default { throw "a-stack install: unsupported architecture: $machine" }
	}
	# PowerShell 5.1 needs TLS 1.2 turned on.
	[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

	if ($version -eq 'latest') { $url = "$repo/latest/download" } else { $url = "$repo/download/$version" }
	$archive = "a-stack_windows_$arch.zip"
	$tmp = Join-Path ([IO.Path]::GetTempPath()) ('a-stack-install-' + [Guid]::NewGuid())
	New-Item -ItemType Directory -Path $tmp | Out-Null
	try {
		Write-Host "Downloading $archive ($version)"
		foreach ($file in $archive, 'SHA256SUMS') {
			try {
				Invoke-WebRequest -UseBasicParsing -Uri "$url/$file" -OutFile (Join-Path $tmp $file)
			} catch {
				throw "a-stack install: download failed: $url/$file"
			}
		}
		$expected = ''
		foreach ($line in Get-Content -LiteralPath (Join-Path $tmp 'SHA256SUMS')) {
			# sha256sum uses a space for text mode and * for binary mode.
			if ($line -match '^([0-9a-fA-F]{64}) [ *](.+)$' -and $Matches[2] -ceq $archive) {
				if ($expected) { throw "a-stack install: duplicate checksum for $archive" }
				$expected = $Matches[1]
			}
		}
		$actual = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $tmp $archive)).Hash
		if (-not $expected -or $expected -ne $actual) {
			throw "a-stack install: checksum mismatch for $archive"
		}
		Expand-Archive -LiteralPath (Join-Path $tmp $archive) -DestinationPath (Join-Path $tmp 'out')
		$new = Join-Path $tmp 'out\a-stack.exe'
		if (-not (Test-Path -LiteralPath $new -PathType Leaf)) {
			throw "a-stack install: $archive has no a-stack.exe"
		}

		$installed = Get-AStackVersion $new
		if (-not $installed) { throw "a-stack install: downloaded binary does not run; existing installation is unchanged" }

		# An existing binary is always replaced, also when it has the same
		# version. Windows cannot replace a running a-stack.exe, but it can
		# rename it: it moves aside to a-stack.exe.old, which the next start
		# of a-stack removes.
		$target = Join-Path $dir 'a-stack.exe'
		$previous = ''
		if (Test-Path -LiteralPath $target -PathType Leaf) { $previous = Get-AStackVersion $target }
		New-Item -ItemType Directory -Force -Path $dir | Out-Null
		if (Test-Path -LiteralPath $target) {
			# An a-stack.exe.old that still runs gets a name of its own.
			$old = "$target.old"
			if (Test-Path -LiteralPath $old) {
				try { Remove-Item -LiteralPath $old -Force } catch { $old = "$target." + [Guid]::NewGuid().ToString('N') + '.old' }
			}
			Move-Item -LiteralPath $target -Destination $old
			try {
				Move-Item -LiteralPath $new -Destination $target
			} catch {
				Move-Item -LiteralPath $old -Destination $target
				throw "a-stack install: cannot replace $target"
			}
		} else {
			Move-Item -LiteralPath $new -Destination $target
		}
		if (-not $previous) {
			Write-Host "Installed a-stack $installed to $target"
		} elseif ($previous -eq $installed) {
			Write-Host "a-stack $installed is already installed in $target"
		} else {
			Write-Host "Updated a-stack $previous -> $installed in $target"
			# Through cmd, so that PowerShell 5.1 does not turn the error
			# output of schtasks into an exception.
			$task = 'a-stack-' + [System.Security.Principal.WindowsIdentity]::GetCurrent().User.Value
			& cmd.exe /c "schtasks /Query /TN $task >nul 2>&1"
			if ($LASTEXITCODE -eq 0) {
				Write-Host "Stop the service: schtasks /End /TN $task"
				Write-Host "After it stops, run: schtasks /Run /TN $task"
			} else {
				Write-Host 'If a-stack serve is running, stop it with Ctrl-C and start it again.'
			}
			Write-Host 'Then run a-stack to open the view.'
		}
	} finally {
		Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
	}

	if ($env:A_STACK_NO_PATH -eq '1') {
		Write-Host "Add $dir to your PATH to run a-stack by name."
	} elseif (Add-AStackPath $dir) {
		Write-Host "Added $dir to your user PATH. Open a new terminal to use it."
	}
	Warn-AStackShadowing $target
	Write-Host 'To teach your coding agents a-stack, run: a-stack skill install'
	Write-Host 'To update a-stack next time, run: a-stack update'
}

# Print the version of an a-stack binary, or nothing when it does not run.
function Get-AStackVersion([string]$exe) {
	try {
		$out = & $exe version 2>$null
		if ($LASTEXITCODE -ne 0) { return '' }
		return ($out | ConvertFrom-Json).data.version
	} catch {
		return ''
	}
}

# Keep existing PATH order (including package-manager installs), but name a
# copy that would run instead. Prepending user PATH cannot beat system PATH.
function Warn-AStackShadowing([string]$target) {
	$command = Get-Command a-stack -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
	if ($command -and [IO.Path]::GetFullPath($command.Source) -ne [IO.Path]::GetFullPath($target)) {
		Write-Warning "Another a-stack is earlier on PATH: $($command.Source). This install is $target. Move its folder earlier on PATH, remove the other copy, or run this one by its full path."
	}
}

# Add dir to the user PATH unless it is there, and to this session. The
# registry value keeps its %VARIABLES% unexpanded. Returns whether the user
# PATH changed.
function Add-AStackPath([string]$dir) {
	$key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
	try {
		$path = [string]$key.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
		$entries = $path -split ';' | Where-Object { $_ } | ForEach-Object { [Environment]::ExpandEnvironmentVariables($_).TrimEnd('\') }
		if ($entries -contains $dir.TrimEnd('\')) { return $false }
		$key.SetValue('Path', (($path.TrimEnd(';'), $dir) -join ';').TrimStart(';'), [Microsoft.Win32.RegistryValueKind]::ExpandString)
	} finally {
		$key.Close()
	}
	# Setting a user variable tells Windows that the environment changed, so
	# that new terminals see the new PATH.
	[Environment]::SetEnvironmentVariable('A_STACK_INSTALL_PING', '1', 'User')
	[Environment]::SetEnvironmentVariable('A_STACK_INSTALL_PING', $null, 'User')
	$env:Path = "$env:Path;$dir"
	return $true
}

# The whole script is read before this line runs, so a cut-off download does nothing.
Install-AStack
