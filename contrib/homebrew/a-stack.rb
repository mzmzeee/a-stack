# Homebrew formula for a-stack on Linux and macOS, from the release archives.
# See contrib/README.md for how to publish a new version.
class AStack < Formula
  desc "Keep an eye on your AI coding agents"
  homepage "https://github.com/mzmzeee/a-stack"
  license "MIT"

  on_macos do
    on_intel do
      url "https://github.com/mzmzeee/a-stack/releases/download/v0.1.9/a-stack_darwin_amd64.tar.gz"
      sha256 "6fea06021d334c74b543d70a972cd7effb6b85efc230023e4fb0a935ec09a9fc"
    end
    on_arm do
      url "https://github.com/mzmzeee/a-stack/releases/download/v0.1.9/a-stack_darwin_arm64.tar.gz"
      sha256 "6fffac7a6d1bf74c38164bc5f0a14d8446cf122871073a77dd0c2f4850c7daa8"
    end
  end

  on_linux do
    on_intel do
      url "https://github.com/mzmzeee/a-stack/releases/download/v0.1.9/a-stack_linux_amd64.tar.gz"
      sha256 "ac30bb16235c198052ab8b540704cfbc5b6829ae10ea64322a5e2bee438d4e31"
    end
    on_arm do
      url "https://github.com/mzmzeee/a-stack/releases/download/v0.1.9/a-stack_linux_arm64.tar.gz"
      sha256 "fe297663b5e8370459c8626eb57b037aded623e45920b77249ae6ee45ce90528"
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
