# Homebrew formula for a-stack on Linux and macOS, from the release archives.
# See contrib/README.md for how to publish a new version.
class AStack < Formula
  desc "Keep an eye on your AI coding agents"
  homepage "https://github.com/mzmzeee/a-stack"
  license "MIT"

  on_macos do
    on_intel do
      url "https://github.com/mzmzeee/a-stack/releases/download/v0.1.8/a-stack_darwin_amd64.tar.gz"
      sha256 "31907dc926b02d37e09578b5330549d96521307b434f7b4c3b224196ec89ff61"
    end
    on_arm do
      url "https://github.com/mzmzeee/a-stack/releases/download/v0.1.8/a-stack_darwin_arm64.tar.gz"
      sha256 "817b711d27c06bb46994001115cf6f000dd6bf7fd0ef259a312cf9bed7642016"
    end
  end

  on_linux do
    on_intel do
      url "https://github.com/mzmzeee/a-stack/releases/download/v0.1.8/a-stack_linux_amd64.tar.gz"
      sha256 "35639aea5616a800b62717caa903b3626d0a0cff89c4ecc7c5bb79d1f2cb4966"
    end
    on_arm do
      url "https://github.com/mzmzeee/a-stack/releases/download/v0.1.8/a-stack_linux_arm64.tar.gz"
      sha256 "1f8d21e1ee73aa4f7675cb89bf143568da30c006cc9ba3a91a5d0a23e564ea5a"
    end
  end

  def install
    bin.install "a-stack"
    generate_completions_from_executable(bin/"a-stack", "completion")
  end

  test do
    assert_match "\"version\":\"v#{version}\"", shell_output("#{bin}/a-stack version")
  end
end
