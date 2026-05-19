output "public_ip" {
  description = "Public IP — point your DNS A record at this."
  value       = aws_eip.iicpc.public_ip
}

output "public_dns" {
  value = aws_instance.iicpc.public_dns
}

output "ssm_command" {
  description = "Open a shell on the VM without SSH."
  value       = "aws ssm start-session --target ${aws_instance.iicpc.id} --region ${var.region}"
}

output "url" {
  value = "http://${aws_eip.iicpc.public_ip}/"
}
