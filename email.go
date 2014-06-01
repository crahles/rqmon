package main

import (
	"bytes"
	"log"
	"net/smtp"
	"strconv"
	"text/template"
	"time"
)

type SmtpTemplateData struct {
	From    string
	To      string
	Subject string
	Body    string
}

const emailTemplate = `From: {{.From}}
To: {{.To}}}
Subject: {{.Subject}}

{{.Body}}
`

func SendAlertEmail(queueName string, lastEmpty time.Time) {
	var err error
	var doc bytes.Buffer

	context := &SmtpTemplateData{
		"from@example.com",
		"to@example.com",
		"RQMon Warning for Queue: " + queueName,
		"Warning: No Zero-Count for " + queueName +
			" since " + strconv.FormatFloat(
			time.Since(lastEmpty).Hours(),
			'f', 0, 64) + " Hours",
	}

	t := template.New("emailTemplate")
	t, err = t.Parse(emailTemplate)
	if err != nil {
		log.Println("error trying to parse mail template")
	}
	err = t.Execute(&doc, context)
	if err != nil {
		log.Println("error trying to execute mail template")
	}

	// Set up authentication information.
	auth := smtp.PlainAuth(
		"",
		"from@example.com",
		"password",
		"smtp.gmail.com",
	)
	// Connect to the server, authenticate, set the sender and recipient,
	// and send the email all in one step.
	err = smtp.SendMail(
		"smtp.gmail.com:587",
		auth,
		"from@example.com",
		[]string{"to@example.com"},
		doc.Bytes(),
	)

	if err != nil {
		log.Printf("Couldn't send Warning for Queue: %s. Error: %s",
			queueName, err.Error())
	}

}
