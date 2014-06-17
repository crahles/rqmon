package main

import (
	"bytes"
	"fmt"
	"log"
	"net/smtp"
	"text/template"
)

type SmtpTemplateData struct {
	From           string
	To             string
	Subject        string
	FailureMessage string
	ResqueLink     string
}

const emailTemplate = `From: {{.From}}
To: {{.To}}
Subject: [RQMon] {{.Subject}}

RQMon noticed the following alert has passed our alerting threshold:

Issue: {{.FailureMessage}}

View Details: {{.ResqueLink}}

Thanks,
Resque Queue Monitoring Daemon
`

func SendAlertByEmail(subject string, failure string, weblink string) {
	var err error
	var doc bytes.Buffer

	context := &SmtpTemplateData{
		*alertFrom,
		*alertRecipient,
		subject,
		failure,
		weblink,
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
	log.Printf("send email alert: %s\n", failure)
}
