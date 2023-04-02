# qemu-guest-kragent

If you're deploying [gokrazy](https://gokrazy.org/) VMs in QEMU and want to
see the [Proxmox PVE](https://www.proxmox.com/en/proxmox-ve) web UI or
whatever to show your VM's IP address(es), use this. It's like the
regular `qemu-guest-kragent` but doesn't do as much.

It's written in Go so works in the gokrazy world: `gok add github.com/bradfitz/qemu-guest-kragent`
to your gokrazy appliance config.


