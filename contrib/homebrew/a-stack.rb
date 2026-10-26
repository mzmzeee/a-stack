# Homebrew formula for a-stack on Linux and macOS, from the release archives.
# See contrib/README.md for how to publish a new version.
class AStack < Formula
  desc "Keep an eye on your AI coding agents"
  homepage "https://github.com/mzmzeee/a-stack"
  license "MIT"

  on_macos do
    on_intel do
      url "https://github.com/mzmzeee/a-stack/releases/download/v0.1.7/a-stack_darwin_amd64.tar.gz"
      sha256 "011613bb1b67e16999246ad19eebcb742c4d0bb71c228fe4d8be6006bb0a0489"
    end
    on_arm do
      url "https://github.com/mzmzeee/a-stack/releases/download/v0.1.7/a-stack_darwin_arm64.tar.gz"
      sha256 "60a5858fbbd49a9408328a630dc534215cab0fd3ca1a6289b8e27e7b396bd363"
    end
  end

  on_linux do
    on_intel do
      url "https://github.com/mzmzeee/a-stack/releases/download/v0.1.7/a-stack_linux_amd64.tar.gz"
      sha256 "c79b19818a87dd2d44aa696fbbae8c925dc8c036aba20128f90bd9bd185e2094"
    end
    on_arm do
      url "https://github.com/mzmzeee/a-stack/releases/download/v0.1.7/a-stack_linux_arm64.tar.gz"
      sha256 "0d2e7c7a6fe003df9f1e303c094d5b7e931f769df79640ee6fc9135cfc21c090"
    end
  end

  def install
    bin.install "a-stack"
  end

  test do
    assert_match "\"version\":\"v#{version}\"", shell_output("#{bin}/a-stack version")
  end
end
