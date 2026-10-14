import subprocess
import sys

import dbus
import dbus.mainloop.glib
from gi.repository import GLib

SOURCE_MONITOR = 1
CURSOR_MODES = {"hidden": 1, "embedded": 2}
BACKGROUND = bytes([0x2E, 0x1E, 0x1E])


def main(cursor_mode, frames, frame_path):
    dbus.mainloop.glib.DBusGMainLoop(set_as_default=True)
    bus = dbus.SessionBus()
    portal = bus.get_object("org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop")
    screencast = dbus.Interface(portal, "org.freedesktop.portal.ScreenCast")
    sender = bus.get_unique_name()[1:].replace(".", "_")
    loop = GLib.MainLoop()
    outcome = {}
    tokens = iter(range(1, 100))

    def fail(message):
        outcome["error"] = message
        loop.quit()

    def request(name, handler, *args):
        token = f"tilde{next(tokens)}"
        path = f"/org/freedesktop/portal/desktop/request/{sender}/{token}"

        def respond(code, results):
            print(f"screencast: {name} answered {code}", flush=True)
            if code != 0:
                fail(f"{name} answered {code} with {dict(results)}")
                return
            handler(results)

        bus.add_signal_receiver(respond, "Response", "org.freedesktop.portal.Request", path=path)
        args[-1]["handle_token"] = token
        getattr(screencast, name)(*args, reply_handler=lambda _: None, error_handler=lambda e: fail(f"{name}: {e}"))

    def started(results):
        streams = results.get("streams", [])
        if len(streams) != 1:
            fail(f"Start gave streams {streams}")
            return
        outcome["node"] = int(streams[0][0])
        outcome["size"] = tuple(int(v) for v in streams[0][1].get("size", (0, 0)))
        loop.quit()

    def selected(_):
        request("Start", started, outcome["session"], "", dbus.Dictionary({}, signature="sv"))

    def created(results):
        outcome["session"] = results["session_handle"]
        options = dbus.Dictionary(
            {"types": dbus.UInt32(SOURCE_MONITOR), "cursor_mode": dbus.UInt32(CURSOR_MODES[cursor_mode]), "multiple": False},
            signature="sv",
        )
        request("SelectSources", selected, outcome["session"], options)

    request("CreateSession", created, dbus.Dictionary({"session_handle_token": "tilde"}, signature="sv"))
    GLib.timeout_add_seconds(60, lambda: fail("the portal never answered"))
    loop.run()
    if "error" in outcome:
        sys.exit(f"screencast: {outcome['error']}")

    width, height = outcome["size"]
    print(f"screencast: stream node {outcome['node']}, {width}x{height}, cursor {cursor_mode}", flush=True)
    remote = screencast.OpenPipeWireRemote(outcome["session"], dbus.Dictionary({}, signature="sv")).take()
    subprocess.run(
        [
            "gst-launch-1.0", "-q",
            "pipewiresrc", f"fd={remote}", f"path={outcome['node']}", f"num-buffers={frames}", "always-copy=true",
            "!", "video/x-raw,format=BGRx",
            "!", "filesink", f"location={frame_path}",
        ],
        check=True,
        timeout=120,
        pass_fds=(remote,),
    )
    with open(frame_path, "rb") as f:
        captured = f.read()
    size = width * height * 4
    if size == 0 or len(captured) != frames * size:
        sys.exit(f"screencast: {len(captured)} bytes for {frames} frames of a {width}x{height} stream")
    distinct = len({captured[at:at + size] for at in range(0, len(captured), size)})
    frame = captured[-size:]
    background = sum(1 for at in range(0, size, 4) if frame[at:at + 3] == BACKGROUND)
    share = background / (width * height)
    print(f"screencast: {distinct} distinct frames of {frames}; {share:.3f} of the last is the desktop background", flush=True)
    if distinct < 2:
        sys.exit("screencast: the stream shows no live content")
    if not 0.1 <= share < 1:
        sys.exit("screencast: the last frame does not show the desktop and its window")

if __name__ == "__main__":
    main(sys.argv[1], int(sys.argv[2]), sys.argv[3])
