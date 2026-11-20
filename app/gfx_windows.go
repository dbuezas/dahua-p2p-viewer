//go:build windows

package main

import "github.com/hajimehoshi/ebiten/v2"

// On the TV PC (Windows 7, old NVIDIA card) Ebiten picked OpenGL by itself, whose
// driver spent ~25% CPU in the kernel; with DirectX the app uses ~3% (measured).
const graphicsLib = ebiten.GraphicsLibraryDirectX
