# dmxbridge

An Art-Net to 433 MHz bridge for effects that only come with an RF remote. It runs on an Orange Pi Zero 3 with a CC1101 module. The Pi receives one Art-Net universe, maps DMX channels to remote button presses, and the CC1101 transmits them. The CC1101 clocks each frame from its own crystal, so Linux timing jitter doesn't affect the RF.

## Wiring (CC1101 numbered header to Zero 3 26-pin)

| CC1101 pin | Signal | Zero 3 pin |
|---|---|---|
| 1 | GND | 6 |
| 2 | VCC | 1 (**3.3 V only**) |
| 3 | GDO0 | 7 (spare) |
| 4 | CSN | 24 (PH9 = SPI1 CS1) |
| 5 | SCK | 23 |
| 6 | MOSI | 19 |
| 7 | MISO | 21 |
| 8 | GDO2 | not connected |

Pin 1 is the square pad, and it should beep for continuity to the SMA shell. Always screw the antenna on before transmitting.

## Enabling SPI

You need SPI1 with **CS1**, because header pin 24 (PH9) is SPI1 CS1. The device then appears as `/dev/spidev1.1`, which is the profile's default. How you turn it on depends on the OS image.

### Orange Pi OS (Arch): `/boot/extlinux/extlinux.conf`

Add an `FDTOVERLAYS` line directly under the `FDT` line. Paths are relative to `/boot`:

```
LABEL Orange Pi
KERNEL /Image
FDT /dtbs/allwinner/sun50i-h618-orangepi-zero3.dtb
FDTOVERLAYS /dtbs/allwinner/overlay/sun50i-h616-spi1-cs1-spidev.dtbo
APPEND initrd=/initramfs-linux.img console=ttyS0,115200 root=PARTUUID=...
```

Don't hand-edit the long `APPEND` line, because if it gets truncated the board won't boot. This inserts only the new line:

```sh
sudo cp /boot/extlinux/extlinux.conf /boot/extlinux/extlinux.conf.bak
sudo sed -i '/^FDT /a FDTOVERLAYS /dtbs/allwinner/overlay/sun50i-h616-spi1-cs1-spidev.dtbo' /boot/extlinux/extlinux.conf
sudo reboot
ls /dev/spidev*        # want /dev/spidev1.1
```

If no spidev device appears, U-Boot ignored `FDTOVERLAYS`. Merge the overlay into the device tree instead:

```sh
sudo pacman -S dtc
cd /boot/dtbs/allwinner
sudo fdtoverlay -i sun50i-h618-orangepi-zero3.dtb \
  -o sun50i-h618-orangepi-zero3-spi.dtb overlay/sun50i-h616-spi1-cs1-spidev.dtbo
# then in extlinux.conf: point FDT at ...-zero3-spi.dtb and delete the FDTOVERLAYS line
```

To roll back, copy `extlinux.conf.bak` back. If the board won't boot, do this from another machine with the SD card mounted.

### Armbian: `/boot/armbianEnv.txt`

```
overlays=spi-spidev spidev1_1
```

### Either OS

- If the device shows up as `spidev1.0` instead, set `radio.spi` in the profile to match.
- The binaries are static, so no runtime packages are needed. For pin debugging, `gpioinfo`/`gpioset` come from `libgpiod` (Arch: `pacman -S libgpiod`; Debian/Armbian: `apt install gpiod`).
- Run `dbctl` and `dmxbridged` with `sudo`, or let a user access the device with a udev rule:
  ```
  # /etc/udev/rules.d/99-spidev.rules
  SUBSYSTEM=="spidev", GROUP="spi", MODE="0660"
  ```
  Then run `sudo groupadd -f spi && sudo usermod -aG spi $USER` and log in again.
- If the chip ID reads all zeros or all 0xFF even though the wiring checks out, the overlay's hardware CS may not be toggling. This is a known problem on some H616 kernels. Move the CSN wire to **pin 26** (PC10) and set `cs_gpio: gpiochip0:74` in the profile. For `probe`, pass `-cs-gpio gpiochip0:74`.

## Build and install

```sh
GOOS=linux GOARCH=arm64 go build -o bin/ ./cmd/...
scp bin/dmxbridged bin/dbctl pi:/usr/local/bin/
scp profiles/holdlamp-1200.yaml pi:/etc/dmxbridge/profile.yaml
scp deploy/dmxbridged.service pi:/etc/systemd/system/
```

## Bring-up, in order

```sh
sudo dbctl probe                 # want: PARTNUM 00 VERSION 14 -> CC1101 OK (see troubleshooting below)
dbctl decode capture.sub         # decode any Flipper RAW capture into keys
sudo dbctl send -cmd 0x24        # LEDs off: a harmless first RF test
sudo dbctl send -cmd 0x04        # LEDs red
sudo dbctl send -cmd 0x01        # FOG ON  (follow with the next line)
sudo dbctl send -cmd 0x03        # FOG OFF
sudo dmxbridged -dry-run -profile /etc/dmxbridge/profile.yaml   # Art-Net path, no RF
sudo systemctl enable --now dmxbridged && journalctl -fu dmxbridged
```

### If `probe` fails

| `probe` reads | Meaning | Check |
|---|---|---|
| `ff ff` | MISO stays high; the chip never answers | power, CS, MISO wire, or a dead chip |
| `00 00` | nothing is driving MISO | MISO wire, power |
| other | wiring is partly right | SCK/MOSI swapped, loose jumpers |

Narrow it down in this order:

1. **Power.** Module pin 2 to pin 1 should read 3.3 V. Idle current should be about 1–2 mA; tens of mA means the chip is shorted.
2. **Pi SPI.** Unplug the module, jumper header pin 19 to pin 21, and run `sudo dbctl loopback`. If it reports OK, the Pi side is fine.
3. **Chip-select.** Move CSN to header pin 26 and run `sudo dbctl probe -cs-gpio gpiochip0:74`. If that works, keep it and set `cs_gpio` in the profile. Use `gpiodetect` to confirm the chip name.
4. If power and loopback pass and software CS still reads `ff ff`, the CC1101 is dead. Swap it.

`gpio readall` (wiringOP) should show pins 19/21/23 as MOSI.1/MISO.1/SCLK.1 and pin 24 as CE.1, all in **ALT4**. If CE.1 shows OFF, the overlay's CS pinmux is broken; use step 3.

### Range and reliability

If the machine misses commands, try these in order: raise `protocol.repeats` (the real remote sends 4 to 8 frames), check the antenna, then raise `radio.power`.

## DMX map (default profile: universe 1, address 1)

| Ch | Name | Behaviour |
|---|---|---|
| 1 | Fog | 0–127 off, 128–255 on. Explicit ON/OFF commands. `max_on` 20 s, then a 10 s cooldown. ON is re-sent every 2 s while on, and OFF every 10 s while idle. |
| 2 | Color | 0 = no change. The other 15 options are spread across 1–255 in hue order. "Off" turns the LEDs off. |
| 3 | Mode | 0 = no change; Jump / Gradual / Pulse / Strobe |
| 4 | Brightness | An absolute fader built from the up/down buttons. On first use it homes by pressing down `steps` times, then steps up to the fader level. Set `steps` to the number of presses from min to max on the real remote. |

If Art-Net stops for longer than 1.5 s, fog goes OFF and color and brightness hold. Stopping the daemon also forces fog OFF.

Radio priority follows channel order: fog commands go ahead of queued colour or brightness presses. While a slider is moving, only the last position is sent.

## QLC+

A ready-made fixture for the default profile is in `qlc/HOLDLAMP-1200W-Fogger-dmxbridge.qxf`. If you change the profile (options, steps, names), regenerate it so QLC+ and the bridge keep the same DMX ranges:

```sh
dbctl qxf -profile /etc/dmxbridge/profile.yaml > HOLDLAMP-1200W-Fogger-dmxbridge.qxf
```

1. **Install the fixture.** Copy the `.qxf` into your QLC+ user fixtures folder and restart QLC+:
   - Windows: `%USERPROFILE%\QLC+\Fixtures\`
   - Linux: `~/.qlcplus/fixtures/`
   - macOS: `~/Library/Application Support/QLC+/Fixtures/`
2. **Patch it.** In Fixtures → Add, choose HOLDLAMP → 1200W Fogger (dmxbridge) → 4 Channel. Put it on **Universe 2**, address 1. Universe 2 in QLC+ is port-address 1 on the wire.
3. **Output.** In Inputs/Outputs, enable Art-Net output on Universe 2 and unicast it to the Pi's IP.
4. **Controls.** The Color channel shows colour swatches in QLC+ 4.12 and later. Build virtual console buttons as usual: Flash for fog, Toggle in a Solo Frame for colours.

The node answers ArtPoll, so it also shows up in DMX-Workshop.

## Remote protocol (HOLDLAMP 1200W)

- 433.657 MHz OOK, TE ≈ 403 µs, 32-bit PWM.
- Sync is 1 TE high + 32 TE low. A 0 bit is 1:3, a 1 bit is 3:1.
- Key = `7CF0` (remote address) · `cmd` · `~cmd`.
- `cmd` is the button number in BCD: 01 fog on, 03 fog off, 04–06 R/G/B, 07–10 jump / gradual / pulse / strobe, 11–12 brightness up/down, 13–24 colors and LEDs off.
- **0x02 has not been captured yet.** Try `dbctl send -cmd 0x02`.

## Layout

```
cmd/dmxbridged     daemon: Art-Net -> engine -> radio
cmd/dbctl          probe | loopback | decode | send | qxf
internal/artnet    ArtDMX receive, ArtPollReply
internal/engine    channel state machines (onoff/select/stepper/trigger), failsafe, TX scheduler
internal/profile   YAML profile, code -> key, DMX range mapping (shared with qxf)
internal/flipper   .sub parser + generic PWM decoder
internal/ook       PWM -> chip stream encoder
internal/cc1101    register setup, FIFO TX with refill
internal/spidev    spidev ioctl + optional GPIO chip-select
```
