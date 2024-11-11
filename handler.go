package main

import (
	"database/sql"
	"errors"
	"log"
	"net/http"

	"github.com/navidrome/navidrome/core/metrics/insights"
)

func handler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var data insights.Data

		err := decodeJSONBody(w, r, &data)
		if err != nil {
			var mr *malformedRequest
			if errors.As(err, &mr) {
				http.Error(w, mr.msg, mr.status)
			} else {
				log.Print(err.Error())
				http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			}
			return
		}

		err = SaveToDB(db, data)
		if err != nil {
			log.Print(err.Error())
		}
		w.WriteHeader(http.StatusOK)
	}
}
