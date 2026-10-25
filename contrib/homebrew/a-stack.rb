# Homebrew formula for a-stack on Linux, from the release archives. See
# contrib/README.md for how to publish a new version.
class AStack < Formula
  desc "Keep an eye on your AI coding agents"
  homepage "https://github.com/mzmzeee/a-stack"
  version "0.1.4"
  license "MIT"

  on_linux do
    on_intel do
      url "https://github.com/mzmzeee/a-stack/releases/download/v0.1.4/a-stack_linux_amd64.tar.gz"
      sha256 "be8c73261bbab152fb52ea58f6e2c23ab092f2041f7e76146987b71a380d29fa"
    end
    on_arm do
      url "https://github.com/mzmzeee/a-stack/releases/download/v0.1.4/a-stack_linux_arm64.tar.gz"
      sha256 "982ac6a72fb2c52a2141eea319a2e48632edc36a72af05ff2e59f9925caf5534"
    end
  end

  depends_on :linux

  def install
    bin.install "a-stack"
  end

  test do
    assert_match "\"version\":\"v#{version}\"", shell_output("#{bin}/a-stack version")
  end
end
