package service

type UserService struct {
}

func NewUserService() *UserService {
	return &UserService{}
}

func (u *UserService) Register(username string, password string) error {
	return nil
}

func (u *UserService) Login(username string, password string) error {
	return nil
}
