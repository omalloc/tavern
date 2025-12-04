Tavern Query
---

### Usage

```bash
# 将 bin/tq 移动到 /usr/bin/tq 或 /usr/local/bin/tq 用于方便执行
mv ./bin/tq /usr/local/bin/tq
chmod +x /usr/local/tq

# 查看日志
tail -f -n 1 ./logs/access.log | tq

# 或 cat
cat ./logs/access.log | tq
```

输出如下结果

```bash
$ tail -f -n 1 ./logs/access.log | tq
(0)Client-Ip: 127.0.0.1:57416
(1)Domain: www.example.com
(2)Content-Type: text/plain;+charset=utf-8
(3)RequestTime: [04/Dec/2025:05:33:28+0000]
(5)Method: GET+http://www.example.com/path/to/1K.bin+HTTP/1.1
(6)ResponseStatus: 500
(7)SentBytes(header+body): 153
(8)Referer: -
(9)UserAgent: curl/8.5.0
(10)ResponseTime(ms): 0
(11)BodySize: 21
(12)ContentLength: -
(13)Range: -
(14)X-Forwarded-For: -
(15)CacheStatus: -
(16)RequestID: d45518ac78d2e9a26dc785217822617d
```
