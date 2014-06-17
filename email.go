package main

import (
	"bytes"
	"fmt"
	"net/smtp"
	"text/template"
)

type SmtpTemplateData struct {
	From    string
	To      string
	Subject string
	Body    string
}

const emailTemplate = `From: {{.From}}
To: {{.To}}
Subject: {{.Subject}}

{{.Body}}
`

func SendAlertByEmail(subject string, body string) {
	var err error
	var doc bytes.Buffer

	context := &SmtpTemplateData{
		*alertFrom,
		*alertRecipient,
		"[RQMon] " + subject,
		body,
	}

	t := template.New("emailTemplate")
	t, err = t.Parse(emailTemplate)
	ok(err)

	err = t.Execute(&doc, context)
	ok(err)

	err = smtp.SendMail(
		fmt.Sprintf("%s:%s", *smtpServer, *smtpPort),
		smtp.PlainAuth(
			"",
			*smtpUsername,
			*smtpPassword,
			*smtpServer,
		),
		*alertFrom,
		[]string{*alertRecipient},
		doc.Bytes(),
	)

	ok(err)
}
