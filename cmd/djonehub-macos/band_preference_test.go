package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestParseBandMaskResponseAndDecodeWideMask(t *testing.T) {
	config, err := parseBandMaskResponse("AT+QCFG=\"band\"\r\n+QCFG: \"band\",0xbff,0x42000001e20b0e18df,0x0\r\nOK")
	if err != nil {
		t.Fatalf("parseBandMaskResponse() err=%v", err)
	}
	if config.GW != "0xbff" || config.LTE != "0x42000001e20b0e18df" || config.TDS != "0x0" {
		t.Fatalf("config=%+v", config)
	}
	bands, err := bandMaskBands(config.LTE)
	if err != nil {
		t.Fatalf("bandMaskBands() err=%v", err)
	}
	want := []int{1, 2, 3, 4, 5, 7, 8, 12, 13, 18, 19, 20, 25, 26, 28, 34, 38, 39, 40, 41, 66, 71}
	if !reflect.DeepEqual(bands, want) {
		t.Fatalf("bands=%v, want %v", bands, want)
	}
}

func TestEncodeAndValidateLTEBands(t *testing.T) {
	mask, err := encodeLTEBandMask([]int{40})
	if err != nil {
		t.Fatalf("encodeLTEBandMask() err=%v", err)
	}
	if mask != "0x8000000000" {
		t.Fatalf("B40 mask=%s", mask)
	}
	selected, mask, err := validateLTEBands([]int{40, 3, 40}, "0x8000000004")
	if err != nil {
		t.Fatalf("validateLTEBands() err=%v", err)
	}
	if !reflect.DeepEqual(selected, []int{3, 40}) || mask != "0x8000000004" {
		t.Fatalf("selected=%v mask=%s", selected, mask)
	}
	if _, _, err := validateLTEBands([]int{7}, "0x8000000004"); err == nil {
		t.Fatal("unsupported B7 was accepted")
	}
}

func TestBandRegistrationParsing(t *testing.T) {
	for _, response := range []string{
		"+CEREG: 0,1\r\nOK",
		"+CEREG: 5\r\nOK",
		"+CEREG: 2,5,\"1234\"\r\nOK",
	} {
		if !parseBandRegistration(response) {
			t.Fatalf("registered response rejected: %q", response)
		}
	}
	if parseBandRegistration("+CEREG: 0,2\r\nOK") {
		t.Fatal("searching registration state was accepted")
	}
	if !parsePacketAttached("+CGATT: 1\r\nOK") || parsePacketAttached("+CGATT: 0\r\nOK") {
		t.Fatal("packet attach parsing is incorrect")
	}
	if got := parseServingBand(`+QNWINFO: "TDD LTE","46015","LTE BAND 40",38950`); got != 40 {
		t.Fatalf("serving band=%d, want 40", got)
	}
}

func TestBuildBandConfigCommandUsesQDC507CompatibleForm(t *testing.T) {
	config := bandMaskConfig{GW: "0xbff", LTE: "0x8000000000", TDS: "0x0"}
	if got, want := buildBandConfigCommand(config), `AT+QCFG="band",0,8000000000`; got != want {
		t.Fatalf("command=%q, want %q", got, want)
	}
}

func TestDemoBandPreferenceAPI(t *testing.T) {
	instance := newDemoApp()
	server := httptest.NewServer(instance.routes())
	defer server.Close()

	response, err := http.Get(server.URL + "/api/network/bands")
	if err != nil {
		t.Fatalf("GET bands: %v", err)
	}
	defer response.Body.Close()
	var initial bandPreferenceResponse
	if err := json.NewDecoder(response.Body).Decode(&initial); err != nil {
		t.Fatalf("decode initial: %v", err)
	}
	if initial.Mode != bandModeAuto || !bandListContains(initial.SupportedBands, 40) {
		t.Fatalf("initial=%+v", initial)
	}

	token := fetchActionToken(t, server.URL)
	request, err := protectedJSONRequest(
		http.MethodPost,
		server.URL+"/api/network/bands",
		token,
		"{\"mode\":\"preferred\",\"bands\":[40]}",
	)
	if err != nil {
		t.Fatalf("build POST bands: %v", err)
	}
	apply, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST bands: %v", err)
	}
	apply.Body.Close()
	if apply.StatusCode != http.StatusAccepted {
		t.Fatalf("POST status=%d", apply.StatusCode)
	}

	deadline := time.Now().Add(time.Second)
	for {
		status, statusErr := instance.currentBandPreferenceResponse()
		if statusErr != nil {
			t.Fatalf("currentBandPreferenceResponse(): %v", statusErr)
		}
		if !status.Operation.InProgress {
			if status.Mode != bandModePreferred || !reflect.DeepEqual(status.EnabledBands, []int{40}) {
				t.Fatalf("status=%+v", status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("demo band operation did not complete")
		}
		time.Sleep(time.Millisecond)
	}
}
