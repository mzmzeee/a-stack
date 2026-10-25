# a-stack plugin for Herdr

This [Herdr](https://herdr.dev) plugin opens the a-stack browser view of one Session, and shows `a-stack watch` for it in a popup. It only runs `a-stack open` and `a-stack watch`, which read and change nothing. It needs `a-stack` on your `PATH` and the a-stack service running.

Install it:

```sh
herdr plugin install mzmzeee/a-stack/contrib/herdr
```

Then tell it your Session. Get the IDs from `a-stack workspace list` and `a-stack session list --workspace ID`, and write them to `a-stack.env` in the plugin's config folder:

```sh
cat >"$(herdr plugin config-dir mzmzeee.a-stack)/a-stack.env" <<'EOF'
A_STACK_WORKSPACE=your-workspace-id
A_STACK_SESSION=your-session-id
EOF
```

The plugin reads this file as a shell file, so keep only these two lines in it.

- The action "Open a-stack view" (`mzmzeee.a-stack.open-view`) opens the view in your browser.
- The pane `watch` prints each change of the Session and rings on a new Needs you: `herdr plugin pane open --plugin mzmzeee.a-stack --entrypoint watch`. Press Ctrl-C to close it.

To bind a key to the action, add this to your Herdr config:

```toml
[[keys.command]]
key = "prefix+a"
type = "plugin_action"
command = "mzmzeee.a-stack.open-view"
description = "open the a-stack view"
```

Like every Herdr plugin, it runs as your user without a sandbox. Read `herdr-plugin.toml` before you install it; it is the whole plugin.
