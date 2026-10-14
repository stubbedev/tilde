const { app, BrowserWindow, desktopCapturer, session } = require("electron");
const path = require("path");

app.whenReady().then(() => {
  session.defaultSession.setDisplayMediaRequestHandler((request, callback) => {
    desktopCapturer.getSources({ types: ["screen"] }).then((sources) => callback({ video: sources[0] }));
  });
  new BrowserWindow({ width: 800, height: 600 }).loadFile(path.join(__dirname, "screencast.html"));
});
