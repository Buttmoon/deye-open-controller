package main

import "log"

func main() {
	app, err := NewApp()
	if err != nil {
		log.Fatal("app init error:", err)
	}
	defer app.Close()

	log.Println("Server started on :8080")
	if err := app.Server().ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
