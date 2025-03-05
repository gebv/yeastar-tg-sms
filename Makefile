playground:
	go test -timeout 120s -run ^Test_Playground$$ github.com/gebv/yeastar-tg-sms/api -v -failfast -count=1 -fuzztime=5s