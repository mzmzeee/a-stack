package stack

import "net/http"

func (s *Service) mailHTTP(w http.ResponseWriter, r *http.Request, token string) bool {
	var request any
	switch r.URL.Path {
	case "/v1/mail/send":
		request = &MailSendRequest{}
	case "/v1/mail/reply":
		request = &MailReplyRequest{}
	case "/v1/mail/recipients":
		request = &MailRecipientsRequest{}
	case "/v1/mail/read":
		request = &MailReadRequest{}
	case "/v1/mail/check":
		request = &MailCheckRequest{}
	default:
		return false
	}
	if r.Method != http.MethodPost {
		writeError(w, failure("invalid_input", "POST JSON required"))
		return true
	}
	if err := requestJSON(w, r, request); err != nil {
		writeError(w, err)
		return true
	}
	var data any
	var err error
	switch q := request.(type) {
	case *MailSendRequest:
		data, err = s.Store.SendMail(r.Context(), token, *q)
	case *MailReplyRequest:
		data, err = s.Store.ReplyMail(r.Context(), token, *q)
	case *MailRecipientsRequest:
		data, err = s.Store.MailRecipients(r.Context(), token, *q)
	case *MailReadRequest:
		data, err = s.Store.ReadMail(r.Context(), token, *q)
	case *MailCheckRequest:
		data, err = s.Store.CheckMail(r.Context(), token, *q)
	}
	if err != nil {
		writeError(w, err)
	} else {
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
	}
	return true
}
