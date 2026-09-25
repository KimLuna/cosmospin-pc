# cosmo-tui

a terminal client for the cosmo app.

## features

- account login
- artist info
- announcements and schedule
- profile and user search
- view and download live replays/room posts
- talk chat with auto-translate
- objekt collection and bulk transfer
- gravity voting

## dependencies

- [go](https://go.dev/)
- [mpv](https://github.com/mpv-player/mpv) (optional, for viewing replays)
- [ffmpeg](https://ffmpeg.org/) (optional, for downloading replays)

## installation

```sh
go install codeberg.org/djvu/cosmo-tui/cmd/cosmo-tui@latest
```

arch linux users can also install from the aur:

```sh
yay -S cosmo-tui-git
```

## configuration

location:

- linux/macos: `~/.config/cosmo-tui/config`
- windows: `%AppData%\cosmo-tui\config`

syntax:

```ini
option=value
```

run `cosmo-tui -h` to see the available options.
