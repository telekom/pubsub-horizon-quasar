package state

import (
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
)

type ResumeToken struct {
	Value string
}

func (t *ResumeToken) Bson() (bson.Raw, error) {
	tokenDoc := bson.M{}
	tokenDoc["_data"] = t.Value
	return bson.Marshal(tokenDoc)
}

func (t *ResumeToken) MarshalJSON() ([]byte, error) {
	value := fmt.Sprintf("\"%s\"", t.Value)
	return []byte(value), nil
}

func (t *ResumeToken) UnmarshalJSON(bytes []byte) error {
	if len(bytes) <= 2 {
		return nil
	}

	t.Value = string(bytes)

	return nil
}
