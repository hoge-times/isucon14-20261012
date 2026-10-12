now = $(shell date "+%Y%m%d%H%M%S")
app = isuride
service = isuride-go.service
godir = go
mysql_auth = 

# ベンチ(練習: ベンチ専用機 i4 から i1 に向けて実行 / 本戦: ポータルから)
.PHONY: bench
bench:
	ssh i4 'cd ~ && ./bench run . run --addr 172.31.0.155:443 --target https://isuride.xiv.isucon.net --payment-url http://172.31.0.144:12346 --payment-bind-port 12346'

# アプリ、nginx、mysql の再起動
.PHONY: re
re:
	make arestart
	make nrestart
	make mrestart
	echo "正常に make re が完了しました"

.PHONY: arestart
arestart:
	sudo systemctl daemon-reload
	sudo systemctl restart ${service}
	sudo systemctl status ${service} --no-pager

.PHONY: nrestart
nrestart:
	sudo touch /var/log/nginx/access.log
	sudo rm /var/log/nginx/access.log
	sudo systemctl reload nginx
	sudo systemctl status nginx --no-pager

.PHONY: mrestart
mrestart:
	sudo touch /var/log/mysql/slow.log
	sudo rm /var/log/mysql/slow.log
	sudo mysqladmin flush-logs ${mysql_auth}
	sudo systemctl restart mysql
	sudo systemctl status mysql --no-pager
	echo "set global slow_query_log = 1;" | sudo mysql ${mysql_auth}
	echo "set global slow_query_log_file = '/var/log/mysql/slow.log';" | sudo mysql ${mysql_auth}
	echo "set global long_query_time = 0;" | sudo mysql ${mysql_auth}

# nginx のアクセスログを alp で集計
.PHONY: nalp
nalp:
	sudo cat /var/log/nginx/access.log | alp ltsv --sort=sum --reverse -m "^/api/app/rides/[0-9A-Z]+/evaluation$$,^/api/chair/rides/[0-9A-Z]+/status$$,^/assets/.*$$,^/images/.*$$"

.PHONY: pt
pt:
	sudo pt-query-digest /var/log/mysql/slow.log > ~/pt.log

.PHONY: ptselect
ptselect:
	sudo pt-query-digest --filter '$$event->{arg} =~ m/^SELECT/' /var/log/mysql/slow.log > ~/pt.log

.PHONY: pprof
pprof:
	curl -o /home/isucon/cpu-profile.prof http://localhost:6060/debug/pprof/profile?seconds=45
	go tool pprof /home/isucon/cpu-profile.prof

.PHONY: build
build: $(wildcard ${godir}/*.go) ${godir}/go.mod ${godir}/go.sum
	cd ${godir} && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o ${app}

define upload
	ssh isucon@$(1) 'sudo systemctl daemon-reload'
	ssh isucon@$(1) 'sudo systemctl stop ${service}'
	scp ./${godir}/${app} isucon@$(1):/home/isucon/webapp/${godir}/${app}
	ssh isucon@$(1) 'sudo systemctl restart ${service}'
	ssh isucon@$(1) 'sudo systemctl status ${service} --no-pager'
endef

.PHONY: upload1 upload2 upload3 all zenbu
upload1: build
	$(call upload,i1)
upload2: build
	$(call upload,i2)
upload3: build
	$(call upload,i3)
all: upload1 upload2 upload3
zenbu: all
	ssh isucon@i1 -A 'cd webapp && make re'
	ssh isucon@i2 -A 'cd webapp && make re'
	ssh isucon@i3 -A 'cd webapp && make re'

.PHONY: pbnalp1 pbnalp2 pbnalp3 pbpt1 pbpt2 pbpt3 pbptselect1 pbptselect2 pbptselect3
pbnalp1: ; ssh isucon@i1 -A "cd webapp && make nalp" | pbcopy
pbnalp2: ; ssh isucon@i2 -A "cd webapp && make nalp" | pbcopy
pbnalp3: ; ssh isucon@i3 -A "cd webapp && make nalp" | pbcopy
pbpt1: ; ssh isucon@i1 -A "cd webapp && make pt && cat ~/pt.log" | pbcopy
pbpt2: ; ssh isucon@i2 -A "cd webapp && make pt && cat ~/pt.log" | pbcopy
pbpt3: ; ssh isucon@i3 -A "cd webapp && make pt && cat ~/pt.log" | pbcopy
pbptselect1: ; ssh isucon@i1 -A "cd webapp && make ptselect && cat ~/pt.log" | pbcopy
pbptselect2: ; ssh isucon@i2 -A "cd webapp && make ptselect && cat ~/pt.log" | pbcopy
pbptselect3: ; ssh isucon@i3 -A "cd webapp && make ptselect && cat ~/pt.log" | pbcopy

.PHONY: getpprof
getpprof:
	scp i1:/home/isucon/cpu-profile.prof ./
	go tool pprof -http 127.0.0.1:9092 ./cpu-profile.prof

.PHONY: upmakefile1 upmakefile2 upmakefile3 gp1 gp2 gp3
upmakefile1: ; scp ./Makefile isucon@i1:/home/isucon/webapp/Makefile
upmakefile2: ; scp ./Makefile isucon@i2:/home/isucon/webapp/Makefile
upmakefile3: ; scp ./Makefile isucon@i3:/home/isucon/webapp/Makefile
gp1: ; ssh isucon@i1 -A "cd webapp && git pull"
gp2: ; ssh isucon@i2 -A "cd webapp && git pull"
gp3: ; ssh isucon@i3 -A "cd webapp && git pull"
