#/bin/sh

DIR=$(dirname $0)

echo "Generating files in $DIR/files"

mkdir -p $DIR/files

dd if=/dev/urandom of=$DIR/files/1B.bin bs=1 count=1
dd if=/dev/urandom of=$DIR/files/1K.bin bs=1K count=1
dd if=/dev/urandom of=$DIR/files/1M.bin bs=1M count=1
dd if=/dev/urandom of=$DIR/files/2M.bin bs=1M count=2
dd if=/dev/urandom of=$DIR/files/5M.bin bs=1M count=5
dd if=/dev/urandom of=$DIR/files/10M.bin bs=1M count=10