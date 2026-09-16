1删除旧文件

D:\unicom_schedule\ngd-ngg-scheduling-demo-git

2拉取新文件

scp -r root@10.112.222.48:/mnt/data0/ngd-ngg-scheduling-demo-git D:\unicom_schedule\

3 进文件夹

cd /d/unicom_schedule/ngd-ngg-scheduling-demo-git

4修正远程地址 

git remote set-url origin "http://gitlab.tianti.tg.unicom.local/inner_source/cucloud/paas-schedbridge.git"
git remote -v

5 把全部改动打包放到暂存

快速看一眼哪些文件准备提交（检查用，可选）

生成一个新版本，写清楚本次做了什么

git add -A
git status --short
git commit -m "feat: replace ngd-ngg-scheduling-demo with current version"

6 强制覆盖远程分支

git push --force origin HEAD:ngd-ngg-scheduling-demo

7 最后确认

git fetch origin
git log origin/ngd-ngg-scheduling-demo --oneline -5
git status

8 核对本地与远程是否完全一致

git fetch origin
git rev-parse HEAD
git rev-parse origin/ngd-ngg-scheduling-demo
git diff --stat HEAD origin/ngd-ngg-scheduling-demo
