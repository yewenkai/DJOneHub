# Third-Party Notices

DJOneHub contains code derived from the upstream VoHive project and retains the license and required notice provided in the repository root [`LICENSE`](LICENSE):

```text
Required Notice: Copyright iniwex5 (https://github.com/iniwex5/vohive)
```

## Release Runtime

The macOS release package includes **libusb 1.0.30**, distributed under the GNU Lesser General Public License, version 2.1 or later.

- Project: <https://libusb.info/>
- Source: <https://github.com/libusb/libusb/releases/tag/v1.0.30>
- License text in the release package: `licenses/libusb-COPYING`

## malgo / miniaudio

The experimental macOS VoLTE audio bridge uses `github.com/gen2brain/malgo`,
Go bindings for the cross-platform miniaudio library.

- malgo source: <https://github.com/gen2brain/malgo>
- malgo license: The Unlicense
- miniaudio source: <https://github.com/mackron/miniaudio>
- miniaudio license: public domain or MIT-0, at the recipient's option

## Vendored Source Dependencies

The source repository includes vendored dependencies under `third_party/` so the versions used by DJOneHub remain reproducible. Their original copyright notices and license texts are retained in the corresponding directories.

| Component | License file |
| --- | --- |
| quectel-qmi-go | `third_party/quectel-qmi-go/LICENSE` |
| strftime | `third_party/strftime/LICENSE` |
| pkg/errors | `third_party/pkg-errors/LICENSE` |
| golang.org/x/sys | `third_party/x-sys/LICENSE` |
| golang.org/x/text | `third_party/x-text/LICENSE` |
| multierr | `third_party/multierr/LICENSE.txt` |

Dependencies fetched through Go modules retain their own licenses and copyright notices. This file is informational and does not replace any component's full license text.

## DJOneHub-mac-enhanced notifier

The native macOS call and SMS notifier is adapted from the notifier in
`rogerbush007-a11y/DJOneHub-mac-enhanced`, distributed under the same PolyForm
Noncommercial License 1.0.0 used by DJOneHub.

- Source: <https://github.com/rogerbush007-a11y/DJOneHub-mac-enhanced>
