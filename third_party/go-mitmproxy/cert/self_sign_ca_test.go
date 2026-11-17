package cert

import (
	"bytes"
	"os"
	"reflect"
	"testing"
)

func TestGetStorePath(t *testing.T) {
	path, err := getStorePath("")
	if err != nil {
		t.Fatal(err)
	}
	if path == "" {
		t.Fatal("should have path")
	}
}

func TestNewCA(t *testing.T) {
	caApi, err := NewSelfSignCA("")
	if err != nil {
		t.Fatal(err)
	}
	ca := caApi.(*SelfSignCA)

	data := make([]byte, 0)
	buf := bytes.NewBuffer(data)

	err = ca.saveTo(buf)
	if err != nil {
		t.Fatal(err)
	}

	fileContent, err := os.ReadFile(ca.caFile())
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(fileContent, buf.Bytes()) {
		t.Fatal("pem content should equal")
	}
}
