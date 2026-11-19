# dahua-p2p-viewer

This was a tiny Windows app that showed all cameras of a Dahua NVR full screen through Dahua's P2P cloud.

**It is deprecated.** It needed the Dahua NetSDK DLLs from SmartPSS Lite and only ran on Windows.
I replaced it with a version in Go that talks to the NVR's P2P tunnel itself (no DLLs) and runs on
Windows 7 and macOS.

The old app, its build script and its README are kept in [`deprecada/`](deprecada/).
