//go:build !ai

package main

// The plain build has no ChatGPT link; build with -tags ai for TeleprompterAI.exe.

func aiStart() {}

func aiMessage(uint32, uintptr, uintptr) bool { return false }
