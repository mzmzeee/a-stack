# Homebrew formula for a-stack on Linux and macOS, from the release archives.
# See contrib/README.md for how to publish a new version.
class AStack < Formula
  desc "Keep an eye on your AI coding agents"
  homepage "https://github.com/mzmzeee/a-stack"
  license "MIT"

  on_macos do
    on_intel do
      url "https://github.com/mzmzeee/a-stack/releases/download/v0.1.6/a-stack_darwin_amd64.tar.gz"
      sha256 "a4dc045bab8a163cb3ec3640dc38d42fcad3cb9e91177c6503873ee451ff5a7e"
    end
    on_arm do
      url "https://github.com/mzmzeee/a-stack/releases/download/v0.1.6/a-stack_darwin_arm64.tar.gz"
      sha256 "618d217cce4388b8e2b8806b42a05f63b59cda014ada8f81a5f35e2c261ab825"
    end
  end

  on_linux do
    on_intel do
      url "https://github.com/mzmzeee/a-stack/releases/download/v0.1.6/a-stack_linux_amd64.tar.gz"
      sha256 "0a28a8c2561009b18956b55e036da31d98eeaf3245d1a32a2431e5c8f598c241"
    end
    on_arm do
      url "https://github.com/mzmzeee/a-stack/releases/download/v0.1.6/a-stack_linux_arm64.tar.gz"
      sha256 "86b8e36a1e49574b53f61a683cfc2cd6ddb24c129bbb67e0a1ed4afb94500e63"
    end
  end

  def install
    bin.install "a-stack"
  end

  test do
    assert_match "\"version\":\"v#{version}\"", shell_output("#{bin}/a-stack version")
  end
end
