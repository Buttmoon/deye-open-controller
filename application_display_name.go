package main

import "strings"

func selectApplicationDisplayName(applicationName, inverterName string) (string, string) {
	if name := strings.TrimSpace(applicationName); name != "" {
		return name, "Имя приложения"
	}
	if name := strings.TrimSpace(inverterName); name != "" {
		return name, "имя первого инвертора (Имя приложения не задано)"
	}
	return "Управление расписанием инверторов", "стандартное имя (Имя приложения и инверторы не заданы)"
}
