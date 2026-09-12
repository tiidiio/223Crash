package main
import ("fmt"; "time")
func main(){
 crash:=12.87
 for m:=1.00; m<=crash; m+=0.05{
  fmt.Printf("\rRund: 1 | crash point : %.2f Multiplicateur : %.2f.....%.2f   ", crash, m, crash)
  time.Sleep(80*time.Millisecond)
 }
 fmt.Println("\nCRASH!")
}
